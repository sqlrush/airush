package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"os/signal"
	"syscall"
	"time"

	"go.opentelemetry.io/otel/trace"

	"github.com/sqlrush/airush/libs/apierror"
	"github.com/sqlrush/airush/libs/obs"
)

// runServer 组装观测/错误中间件链并带优雅退出运行（k8s preStop 契约的雏形）。
func runServer(cfg appConfig, provider *obs.Provider, version string) error {
	mux := http.NewServeMux()
	mux.HandleFunc("/healthz", func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusOK)
		_, _ = fmt.Fprintf(w, "ok %s %s\n", component, version)
	})
	mux.Handle("/demo", apierror.Middleware(demoHandler))

	srv := &http.Server{
		Addr:              cfg.Listen,
		Handler:           obs.HTTPMiddleware(component, mux),
		ReadHeaderTimeout: 5 * time.Second,
	}

	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer stop()

	errCh := make(chan error, 1)
	go func() { errCh <- srv.ListenAndServe() }()
	provider.Logger.Info("gateway serving", "listen", cfg.Listen)

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
	provider.Logger.Info("gateway stopped")
	return nil
}

// demoHandler 一次请求产生三信号（spec-0.9 D5）：结构化日志（含 trace_id）、
// server span（中间件）、请求指标（中间件）；?fail=quota 演示错误码路径。
func demoHandler(w http.ResponseWriter, r *http.Request) error {
	logger := obs.LoggerFrom(r.Context())

	if r.URL.Query().Get("fail") == "quota" {
		return apierror.New(apierror.CodeQuotaExceeded)
	}
	if r.URL.Query().Get("fail") == "panic" {
		panic("demo panic path")
	}

	logger.Info("demo event",
		"note", "three-signals probe",
		"redaction_probe", "password=should-not-appear")

	traceID := trace.SpanContextFromContext(r.Context()).TraceID().String()
	w.Header().Set("Content-Type", "application/json")
	if err := json.NewEncoder(w).Encode(map[string]string{
		"ok": "true", "trace_id": traceID,
	}); err != nil {
		return errors.Join(errors.New("encode demo response"), err)
	}
	return nil
}
