package main

import (
	"context"
	"fmt"
	"net/http"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/sqlrush/airush/console/internal/collector"
	"github.com/sqlrush/airush/console/internal/credcrypto"
	"github.com/sqlrush/airush/console/internal/directconn"
	"github.com/sqlrush/airush/console/internal/httpapi"
	"github.com/sqlrush/airush/console/internal/pki"
	"github.com/sqlrush/airush/console/internal/repo"
	"github.com/sqlrush/airush/console/internal/svcapi"
	"github.com/sqlrush/airush/libs/metrics"
	"github.com/sqlrush/airush/libs/obs"
)

// metricsSinkCapacity 是 console 侧 Direct 采集环形 Sink 的保留批数（Stage 1 buffer）。
const metricsSinkCapacity = 256

// pkiInitMain 生成新内部 CA 并输出 PEM（运维一次性执行，产物入 k8s Secret）。
func pkiInitMain() {
	certPEM, keyPEM, err := pki.Generate("airush-connector-ca")
	if err != nil {
		fmt.Fprintln(os.Stderr, "error:", err)
		os.Exit(1)
	}
	fmt.Printf("%s%s", certPEM, keyPEM)
}

// serveMain 校验 --serve 必需配置并启动服务（main 的装配出口）。
func serveMain(cfg appConfig) {
	for env, v := range map[string]string{
		"AIRUSH_CONSOLE_DB_URL":         cfg.DBURL,
		"AIRUSH_CONSOLE_CREDENTIAL_KEK": cfg.CredentialKEK,
		"AIRUSH_CONSOLE_SVC_TOKEN":      cfg.SvcToken,
		"AIRUSH_CONSOLE_CA_CERT":        cfg.CACert,
		"AIRUSH_CONSOLE_CA_KEY":         cfg.CAKey,
	} {
		if v == "" {
			fmt.Fprintf(os.Stderr, "error: %s 未设置（--serve 必需）\n", env)
			os.Exit(2)
		}
	}
	provider := obs.Init(context.Background(), obs.Config{
		Component:    component,
		OTLPEndpoint: cfg.OTLPEndpoint,
		SampleRatio:  cfg.SampleRatio,
		LogLevel:     cfg.LogLevel,
	})
	if err := runServer(cfg, provider, version); err != nil {
		fmt.Fprintln(os.Stderr, "error:", err)
		os.Exit(1)
	}
}

// buildDirect 装配凭据加密器 + 直连接入器连接池（runServer 与采集器共享同一实例）。
func buildDirect(cfg appConfig, store *repo.Store) (*credcrypto.Sealer, *directconn.Manager, error) {
	sealer, err := credcrypto.New(cfg.CredentialKEK, cfg.CredentialKEKID)
	if err != nil {
		return nil, nil, fmt.Errorf("init credential sealer: %w", err)
	}
	directCfg := directconn.DefaultConfig()
	if cfg.DirectIdleTTL > 0 {
		directCfg.IdleTTL = cfg.DirectIdleTTL
	}
	if cfg.DirectConnectTimeout > 0 {
		directCfg.ConnectTimeout = cfg.DirectConnectTimeout
	}
	return sealer, directconn.New(store, sealer, directCfg), nil
}

// buildHandler 组装路由面：公开 API（/api/v1）+ 服务间内部 API（/internal/v1）+ healthz。
func buildHandler(cfg appConfig, store *repo.Store, sealer *credcrypto.Sealer, direct *directconn.Manager, version string) (http.Handler, error) {
	api, err := httpapi.New(store, sealer, direct, cfg.DefaultTenantID)
	if err != nil {
		return nil, fmt.Errorf("init httpapi: %w", err)
	}
	ca, err := pki.Load([]byte(cfg.CACert), []byte(cfg.CAKey))
	if err != nil {
		return nil, fmt.Errorf("load connector CA: %w", err)
	}
	svc := svcapi.New(store, ca, cfg.SvcToken)

	mux := http.NewServeMux()
	mux.HandleFunc("/healthz", func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusOK)
		_, _ = fmt.Fprintf(w, "ok %s %s\n", component, version)
	})
	mux.Handle("/api/v1/", api.Handler())
	mux.Handle("/internal/v1/", svc.Handler())
	return mux, nil
}

// startCollector 启动指标采集调度器（spec-1.3 D3）：Direct 通道本地探针，Connector
// 通道经 gateway 触发（GatewayURL 配置时）。ctx 取消即停。Stage 1 Sink 为内存 buffer。
func startCollector(ctx context.Context, cfg appConfig, store *repo.Store, direct *directconn.Manager, provider *obs.Provider) {
	var connCollector collector.ConnectorCollector
	if cfg.GatewayURL != "" {
		connCollector = collector.NewGatewayClient(cfg.GatewayURL, cfg.SvcToken)
	} else {
		provider.Logger.Info("collector: connector path disabled (no GATEWAY_URL); direct-only")
	}
	sink := metrics.NewBufferSink(metricsSinkCapacity)
	ccfg := collector.DefaultConfig()
	if cfg.MetricsInterval > 0 {
		ccfg.Interval = cfg.MetricsInterval
	}
	c := collector.New(store, direct, connCollector, sink, ccfg, cfg.DefaultTenantID, provider.Logger)
	go c.Run(ctx)
	provider.Logger.Info("collector started", "interval", ccfg.Interval, "connector_path", cfg.GatewayURL != "")
}

// runServer 装配控制面 API 服务（spec-1.1 D2）：repo 基座 + 凭据加密 + httpapi
// 路由，外层套 obs 观测中间件，带优雅退出。
func runServer(cfg appConfig, provider *obs.Provider, version string) error {
	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer stop()

	store, err := repo.New(ctx, cfg.DBURL)
	if err != nil {
		return fmt.Errorf("init repo store: %w", err)
	}
	defer store.Close()

	sealer, direct, err := buildDirect(cfg, store)
	if err != nil {
		return err
	}
	defer direct.Close()

	mux, err := buildHandler(cfg, store, sealer, direct, version)
	if err != nil {
		return err
	}

	// spec-1.3：指标采集调度器（后台）；ctx 取消即停各采集循环。
	startCollector(ctx, cfg, store, direct, provider)

	srv := &http.Server{
		Addr:              cfg.Listen,
		Handler:           obs.HTTPMiddleware(component, mux),
		ReadHeaderTimeout: 5 * time.Second,
	}

	errCh := make(chan error, 1)
	go func() { errCh <- srv.ListenAndServe() }()
	provider.Logger.Info("console serving", "listen", cfg.Listen)

	select {
	case err := <-errCh:
		return fmt.Errorf("serve: %w", err)
	case <-ctx.Done():
	}

	shutdownCtx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	if err := srv.Shutdown(shutdownCtx); err != nil {
		return fmt.Errorf("graceful shutdown: %w", err)
	}
	provider.Shutdown(shutdownCtx)
	provider.Logger.Info("console stopped")
	return nil
}
