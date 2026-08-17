package main

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"os"
	"os/signal"
	"strings"
	"syscall"
	"time"

	"github.com/sqlrush/codexgo/pkg/config"
	"github.com/sqlrush/codexgo/pkg/mcp"

	"github.com/sqlrush/airush/agent-runtime/internal/api"
	"github.com/sqlrush/airush/agent-runtime/internal/pgstore"
	"github.com/sqlrush/airush/agent-runtime/internal/runtime"
	"github.com/sqlrush/airush/agent-runtime/internal/scheduler"
	"github.com/sqlrush/airush/libs/llm"
	"github.com/sqlrush/airush/libs/obs"
)

// runServer 装配并运行：pgstore → 恢复扫描 → MCP → Meter → Engine → sweeper → HTTP；
// SIGTERM 后先排水（停领取 → 等在飞 turn ≤ DRAIN_TIMEOUT）再关 HTTP。
func runServer(cfg appConfig, provider *obs.Provider, version string) error {
	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer stop()
	logger := provider.Logger

	store, err := pgstore.Open(ctx, cfg.DBURL, pgstore.Options{DefaultModel: cfg.DefaultModel})
	if err != nil {
		return fmt.Errorf("open store: %w", err)
	}
	defer store.Close()
	if err := store.EnsureEventPartitions(ctx); err != nil {
		return fmt.Errorf("ensure event partitions: %w", err)
	}

	mcpManager := startMCP(ctx, cfg, logger)
	var mcpGateway runtime.MCPGateway
	if mcpManager != nil {
		defer mcpManager.Shutdown()
		mcpGateway = mcpManager
	}

	console := llm.NewConsoleClient(cfg.ConsoleURL, cfg.SvcToken)
	meter := llm.NewMeter(nil, console, console, llm.WithMasterKey(cfg.LLMKey), llm.WithLogger(logger))

	engine, err := runtime.New(runtime.Config{
		Store:        store,
		DefaultModel: cfg.DefaultModel,
		LLMBaseURL:   cfg.LLMURL,
		LLMTransport: meter,
		MCP:          mcpGateway,
		PodName:      podName(),
		Logger:       logger,
	})
	if err != nil {
		return fmt.Errorf("build engine: %w", err)
	}
	engine.SetLimiter(scheduler.NewTenantLimiter(cfg.MaxConcurrentTurns))
	if n, err := engine.Recover(ctx); err != nil {
		return fmt.Errorf("recover orphan threads: %w", err)
	} else if n > 0 {
		logger.Info("recovered orphan threads", "count", n)
	}

	sweepCtx, stopSweep := context.WithCancel(ctx)
	defer stopSweep()
	go scheduler.NewSweeper(store, engine, 0, logger).Run(sweepCtx)

	mux := http.NewServeMux()
	mux.HandleFunc("/healthz", func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusOK)
		_, _ = fmt.Fprintf(w, "ok %s %s\n", component, version)
	})
	// readyz 在排水期间返回 503：Service 摘除本 pod（k8s 也会因 terminating 摘除，双保险）。
	mux.HandleFunc("/readyz", func(w http.ResponseWriter, _ *http.Request) {
		if engine.Draining() {
			w.WriteHeader(http.StatusServiceUnavailable)
			_, _ = fmt.Fprintln(w, "draining")
			return
		}
		w.WriteHeader(http.StatusOK)
		_, _ = fmt.Fprintln(w, "ready")
	})
	mux.Handle("/internal/v1/agent/", api.New(engine, engine, cfg.SvcToken).Handler())

	srv := &http.Server{
		Addr:              cfg.Listen,
		Handler:           obs.HTTPMiddleware(component, mux),
		ReadHeaderTimeout: 5 * time.Second,
	}
	errCh := make(chan error, 1)
	go func() { errCh <- srv.ListenAndServe() }()
	logger.Info("agent-runtime serving", "listen", cfg.Listen, "pod", podName(), "mcp_servers", mcpServerCount(mcpManager))

	select {
	case err := <-errCh:
		return fmt.Errorf("serve: %w", err)
	case <-ctx.Done():
	}

	// 排水（D5）：停领取 → 等在飞 turn → 超时中断标 interrupted。
	drainCtx, cancelDrain := context.WithTimeout(context.Background(), cfg.DrainTimeout+10*time.Second)
	defer cancelDrain()
	stopSweep()
	engine.Drain(drainCtx, cfg.DrainTimeout)

	shutdownCtx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	if err := srv.Shutdown(shutdownCtx); err != nil && !errors.Is(err, http.ErrServerClosed) {
		return fmt.Errorf("graceful shutdown: %w", err)
	}
	provider.Shutdown(shutdownCtx)
	logger.Info("agent-runtime stopped")
	return nil
}

// podName 取 k8s downward API 注入的 POD_NAME，缺省主机名。
func podName() string {
	if v := os.Getenv("POD_NAME"); v != "" {
		return v
	}
	h, err := os.Hostname()
	if err != nil || h == "" {
		return "agent-runtime"
	}
	return h
}

// parseMCPEndpoints 解析 "name=url,name2=url2"（空 → 无）。
func parseMCPEndpoints(raw string) (map[string]config.McpServerConfig, error) {
	out := map[string]config.McpServerConfig{}
	if strings.TrimSpace(raw) == "" {
		return out, nil
	}
	for _, part := range strings.Split(raw, ",") {
		part = strings.TrimSpace(part)
		if part == "" {
			continue
		}
		name, url, ok := strings.Cut(part, "=")
		name, url = strings.TrimSpace(name), strings.TrimSpace(url)
		if !ok || name == "" || !(strings.HasPrefix(url, "http://") || strings.HasPrefix(url, "https://")) {
			return nil, fmt.Errorf("AIRUSH_AGENT_MCP_ENDPOINTS 项 %q 不是 name=http(s)://url", part)
		}
		if _, dup := out[name]; dup {
			return nil, fmt.Errorf("AIRUSH_AGENT_MCP_ENDPOINTS 重复的 server 名 %q", name)
		}
		out[name] = config.McpServerConfig{
			Transport: config.McpServerTransportConfig{Kind: config.McpTransportStreamableHTTP, URL: url},
			Enabled:   true,
		}
	}
	return out, nil
}

// startMCP 启动静态 MCP endpoints（无 → nil）。单个 server 起不来只告警：skill 不可用不该
// 让整个运行时起不来（对话仍可进行）。
func startMCP(ctx context.Context, cfg appConfig, logger interface {
	Warn(string, ...any)
	Info(string, ...any)
}) *mcp.Manager {
	servers, _ := parseMCPEndpoints(cfg.MCPEndpoints)
	if len(servers) == 0 {
		return nil
	}
	mgr, results, err := mcp.NewManager(ctx, servers, mcp.ManagerOptions{StoreMode: config.OAuthCredentialsStoreFile})
	if err != nil {
		logger.Warn("mcp manager unavailable; skills disabled", "error", err)
		return nil
	}
	for _, r := range results {
		if r.Status == mcp.StartupFailed {
			logger.Warn("mcp server failed to start", "server", r.ServerName, "error", r.Err)
		} else {
			logger.Info("mcp server ready", "server", r.ServerName)
		}
	}
	return mgr
}

func mcpServerCount(m *mcp.Manager) int {
	if m == nil {
		return 0
	}
	return len(m.ListAllToolInfos())
}
