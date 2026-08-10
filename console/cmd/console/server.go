package main

import (
	"context"
	"fmt"
	"net/http"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/sqlrush/airush/console/internal/credcrypto"
	"github.com/sqlrush/airush/console/internal/httpapi"
	"github.com/sqlrush/airush/console/internal/repo"
	"github.com/sqlrush/airush/libs/obs"
)

// serveMain 校验 --serve 必需配置并启动服务（main 的装配出口）。
func serveMain(cfg appConfig) {
	for env, v := range map[string]string{
		"AIRUSH_CONSOLE_DB_URL":         cfg.DBURL,
		"AIRUSH_CONSOLE_CREDENTIAL_KEK": cfg.CredentialKEK,
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

	sealer, err := credcrypto.New(cfg.CredentialKEK, cfg.CredentialKEKID)
	if err != nil {
		return fmt.Errorf("init credential sealer: %w", err)
	}

	api, err := httpapi.New(store, sealer, cfg.DefaultTenantID)
	if err != nil {
		return fmt.Errorf("init httpapi: %w", err)
	}

	mux := http.NewServeMux()
	mux.HandleFunc("/healthz", func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusOK)
		_, _ = fmt.Fprintf(w, "ok %s %s\n", component, version)
	})
	mux.Handle("/api/v1/", api.Handler())

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
