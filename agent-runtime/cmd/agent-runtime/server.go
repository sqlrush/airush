package main

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
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

	mcpManager := startMCP(ctx, cfg, logger)
	if mcpManager != nil {
		defer mcpManager.Shutdown()
	}
	engine, err := buildEngine(ctx, cfg, store, mcpManager, logger)
	if err != nil {
		return err
	}
	sweepCtx, stopSweep := context.WithCancel(ctx)
	defer stopSweep()
	go scheduler.NewSweeper(store, engine, 0, logger).Run(sweepCtx)

	srv := &http.Server{
		Addr:              cfg.Listen,
		Handler:           obs.HTTPMiddleware(component, buildMux(cfg, engine, version)),
		ReadHeaderTimeout: 5 * time.Second,
	}
	errCh := make(chan error, 1)
	go func() { errCh <- srv.ListenAndServe() }()
	logger.Info("agent-runtime serving", "listen", cfg.Listen, "pod", podName(), "mcp_tools", mcpToolCount(mcpManager))

	select {
	case err := <-errCh:
		return fmt.Errorf("serve: %w", err)
	case <-ctx.Done():
	}
	stopSweep()
	return drainAndStop(cfg, engine, srv, provider)
}

// buildEngine 装配 Meter + Engine + 租户并发上限，并跑一次启动期恢复扫描。
func buildEngine(ctx context.Context, cfg appConfig, store *pgstore.Store, mcpManager *mcp.Manager, logger *slog.Logger) (*runtime.Engine, error) {
	var mcpGateway runtime.MCPGateway
	if mcpManager != nil {
		mcpGateway = mcpManager
	}
	console := llm.NewConsoleClient(cfg.ConsoleURL, cfg.SvcToken)
	meter := llm.NewMeter(nil, console, console, llm.WithMasterKey(cfg.LLMKey), llm.WithLogger(logger))
	wire, err := runtime.ParseWireAPI(cfg.LLMWireAPI)
	if err != nil {
		return nil, err
	}
	engine, err := runtime.New(runtime.Config{
		Store:        store,
		DefaultModel: cfg.DefaultModel,
		LLMBaseURL:   cfg.LLMURL,
		LLMTransport: meter,
		LLMWireAPI:   wire,
		MCP:          mcpGateway,
		PodName:      podName(),
		Logger:       logger,
	})
	if err != nil {
		return nil, fmt.Errorf("build engine: %w", err)
	}
	engine.SetLimiter(scheduler.NewTenantLimiter(cfg.MaxConcurrentTurns))
	// 启动期维护（分区预建 + 孤儿恢复）放后台重试：首次安装时 0006 迁移是 post-install hook，
	// 比本 pod 晚跑；启动时硬失败会让 helm --wait 与迁移互相等（同 console：不以 schema 门就绪）。
	go runtime.Maintain(ctx, engine, logger)
	return engine, nil
}

// buildMux 组装路由：healthz / readyz（排水期间 503，Service 摘流量的双保险）/ 内部 API。
func buildMux(cfg appConfig, engine *runtime.Engine, version string) *http.ServeMux {
	mux := http.NewServeMux()
	mux.HandleFunc("/healthz", func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusOK)
		_, _ = fmt.Fprintf(w, "ok %s %s\n", component, version)
	})
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
	return mux
}

// drainAndStop 是 SIGTERM 之后的收尾：排水（D5）→ 关 HTTP → 关观测。
func drainAndStop(cfg appConfig, engine *runtime.Engine, srv *http.Server, provider *obs.Provider) error {
	drainCtx, cancelDrain := context.WithTimeout(context.Background(), cfg.DrainTimeout+10*time.Second)
	defer cancelDrain()
	engine.Drain(drainCtx, cfg.DrainTimeout)

	shutdownCtx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	if err := srv.Shutdown(shutdownCtx); err != nil && !errors.Is(err, http.ErrServerClosed) {
		return fmt.Errorf("graceful shutdown: %w", err)
	}
	provider.Shutdown(shutdownCtx)
	provider.Logger.Info("agent-runtime stopped")
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
		if !ok || name == "" || (!strings.HasPrefix(url, "http://") && !strings.HasPrefix(url, "https://")) {
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
func startMCP(ctx context.Context, cfg appConfig, logger *slog.Logger) *mcp.Manager {
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

func mcpToolCount(m *mcp.Manager) int {
	if m == nil {
		return 0
	}
	return len(m.ListAllToolInfos())
}
