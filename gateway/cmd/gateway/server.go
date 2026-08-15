package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net"
	"net/http"
	"os/signal"
	"syscall"
	"time"

	"go.opentelemetry.io/otel/trace"

	"github.com/sqlrush/airush/gateway/internal/accept"
	"github.com/sqlrush/airush/gateway/internal/consoleclient"
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

	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer stop()

	errCh := make(chan error, 1)

	// spec-1.2：接入面（注册/会话 gRPC）与 HTTP 观测面同进程；ConsoleURL 空则跳过
	// （保留纯观测演示形态供 Stage 0 验收链路复用）。先装配接入面，才能把内部
	// 采集 API（spec-1.3 D4）挂到同一 mux（HTTP 服务尚未 ListenAndServe，改路由安全）。
	accepter, err := startAccept(ctx, cfg, provider, errCh)
	if err != nil {
		return err
	}
	if accepter != nil {
		mux.Handle("/internal/v1/collect", accept.CollectHandler(accepter, cfg.SvcToken))
	}

	srv := &http.Server{
		Addr:              cfg.Listen,
		Handler:           obs.HTTPMiddleware(component, mux),
		ReadHeaderTimeout: 5 * time.Second,
	}
	go func() { errCh <- srv.ListenAndServe() }()
	provider.Logger.Info("gateway serving", "listen", cfg.Listen)

	select {
	case err := <-errCh:
		return fmt.Errorf("serve: %w", err)
	case <-ctx.Done():
	}

	if accepter != nil {
		accepter.GracefulStop("gateway shutting down")
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

// startAccept 装配并启动 Connector 接入面；ConsoleURL 空时返回 nil（观测演示形态）。
func startAccept(_ context.Context, cfg appConfig, provider *obs.Provider, errCh chan error) (*accept.Servers, error) {
	if cfg.ConsoleURL == "" {
		provider.Logger.Info("accept disabled (no CONSOLE_URL); observability-only mode")
		return nil, nil
	}
	console := consoleclient.New(cfg.ConsoleURL, cfg.SvcToken)
	// Connector DataUpload 出口（spec-1.5 §8 Q5-A）：转发给 console 落库。
	// gateway 自身不持 DB 连接——它面向客户侧 Connector，爆炸半径值得保住。
	servers, err := accept.Build(console, accept.TLSMaterial{
		ServerCertPEM: []byte(cfg.TLSCertPEM),
		ServerKeyPEM:  []byte(cfg.TLSKeyPEM),
		ClientCAPEM:   []byte(cfg.ClientCAPEM),
	}, accept.DefaultSessionConfig(), accept.Deps{
		Logger: provider.Logger, Uploader: console,
	})
	if err != nil {
		return nil, fmt.Errorf("build accept servers: %w", err)
	}
	enrollLn, err := net.Listen("tcp", cfg.EnrollListen)
	if err != nil {
		return nil, fmt.Errorf("listen enroll: %w", err)
	}
	sessionLn, err := net.Listen("tcp", cfg.SessionListen)
	if err != nil {
		return nil, fmt.Errorf("listen session: %w", err)
	}
	go func() { errCh <- servers.Serve(enrollLn, sessionLn) }()
	provider.Logger.Info("accept serving", "enroll", cfg.EnrollListen, "session", cfg.SessionListen)
	return servers, nil
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
