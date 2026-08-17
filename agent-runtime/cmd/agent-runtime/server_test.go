package main

import (
	"context"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/sqlrush/airush/agent-runtime/internal/pgstore"
	"github.com/sqlrush/airush/agent-runtime/internal/runtime"
	"github.com/sqlrush/airush/libs/obs"
)

// newIdleEngine 装一个不碰数据库的 Engine（无在飞线程时 Drain/Draining 不查库）。
func newIdleEngine(t *testing.T) *runtime.Engine {
	t.Helper()
	e, err := runtime.New(runtime.Config{
		Store: pgstore.New(nil, pgstore.Options{}), LLMBaseURL: "http://llm.invalid/v1", LLMTransport: http.DefaultTransport,
		Logger: slog.New(slog.NewTextHandler(io.Discard, nil)),
	})
	if err != nil {
		t.Fatalf("engine: %v", err)
	}
	return e
}

func TestBuildMuxProbesAndDrainAndStop(t *testing.T) {
	e := newIdleEngine(t)
	cfg := appConfig{SvcToken: "tok", DrainTimeout: time.Second}
	srv := httptest.NewServer(buildMux(cfg, e, "1.2.3"))
	defer srv.Close()

	get := func(path string) int {
		resp, err := http.Get(srv.URL + path)
		if err != nil {
			t.Fatalf("get %s: %v", path, err)
		}
		_ = resp.Body.Close()
		return resp.StatusCode
	}
	if get("/healthz") != 200 || get("/readyz") != 200 {
		t.Fatal("probes must be 200 before drain")
	}
	// 内部 API 挂上了且认证生效
	if get("/internal/v1/agent/threads") != 401 {
		t.Fatal("internal api must require svc token")
	}

	provider := obs.Init(context.Background(), obs.Config{Component: component, LogLevel: "error"})
	httpSrv := &http.Server{Addr: "127.0.0.1:0", ReadHeaderTimeout: time.Second}
	if err := drainAndStop(cfg, e, httpSrv, provider); err != nil {
		t.Fatalf("drainAndStop: %v", err)
	}
	if !e.Draining() || get("/readyz") != 503 || get("/healthz") != 200 {
		t.Fatal("after drain: readyz must be 503, healthz 200")
	}
}

func TestStartMCPWithoutEndpoints(t *testing.T) {
	logger := slog.New(slog.NewTextHandler(io.Discard, nil))
	if m := startMCP(context.Background(), appConfig{}, logger); m != nil {
		t.Fatal("no endpoints → nil manager")
	}
	if mcpToolCount(nil) != 0 {
		t.Fatal("nil manager → 0 tools")
	}
}
