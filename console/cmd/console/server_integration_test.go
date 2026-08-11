//go:build integration

package main

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net"
	"net/http"
	"syscall"
	"testing"
	"time"

	"github.com/sqlrush/airush/console/internal/dbmigrate"
	"github.com/sqlrush/airush/libs/obs"
	"github.com/sqlrush/airush/testkit"
)

// TestRunServerLifecycle 控制面服务全链路：真 PG + 迁移 + 启动 → healthz →
// API 请求（走 obs/apierror/tenant 全中间件链）→ SIGTERM 优雅退出。
func TestRunServerLifecycle(t *testing.T) {
	ctx := context.Background()

	pg, err := testkit.StartPostgres(ctx)
	if err != nil {
		t.Fatalf("start postgres: %v", err)
	}
	t.Cleanup(func() { _ = pg.Terminate(context.Background()) })
	if err := dbmigrate.RunWithURL(pg.ConnString, []string{"up"}); err != nil {
		t.Fatalf("migrate up: %v", err)
	}

	cfg := appConfig{
		LogLevel:        "info",
		Listen:          fmt.Sprintf("127.0.0.1:%d", freePort(t)),
		SampleRatio:     1.0,
		DBURL:           pg.ConnString,
		CredentialKEK:   "MDEyMzQ1Njc4OWFiY2RlZjAxMjM0NTY3ODlhYmNkZWY=",
		CredentialKEKID: "v1",
		DefaultTenantID: "00000000-0000-0000-0000-000000000001",
	}
	provider := obs.Init(ctx, obs.Config{
		Component: component, LogLevel: cfg.LogLevel, SampleRatio: cfg.SampleRatio,
	})

	done := make(chan error, 1)
	go func() { done <- runServer(cfg, provider, "test") }()

	base := "http://" + cfg.Listen
	waitHealthz(t, base)

	// 经全中间件链创建 + 列表（seed 租户上下文生效即 RLS 路径生效）
	resp, err := http.Post(base+"/api/v1/agents", "application/json",
		jsonBody(`{"name":"lifecycle-agent","kind":"assistant"}`))
	if err != nil || resp.StatusCode != http.StatusCreated {
		t.Fatalf("create agent: %v status=%v", err, statusOf(resp))
	}
	_ = resp.Body.Close()

	resp, err = http.Get(base + "/api/v1/agents")
	if err != nil || resp.StatusCode != http.StatusOK {
		t.Fatalf("list agents: %v status=%v", err, statusOf(resp))
	}
	var page struct {
		Items []map[string]any `json:"items"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&page); err != nil || len(page.Items) != 1 {
		t.Fatalf("list decode: %v items=%d", err, len(page.Items))
	}
	_ = resp.Body.Close()

	if err := syscall.Kill(syscall.Getpid(), syscall.SIGTERM); err != nil {
		t.Fatalf("send SIGTERM: %v", err)
	}
	select {
	case err := <-done:
		if err != nil {
			t.Fatalf("runServer returned error: %v", err)
		}
	case <-time.After(15 * time.Second):
		t.Fatal("graceful shutdown timeout")
	}
}

func jsonBody(s string) io.Reader { return &jsonReader{s: s} }

type jsonReader struct{ s string }

func (r *jsonReader) Read(p []byte) (int, error) {
	if r.s == "" {
		return 0, io.EOF
	}
	n := copy(p, r.s)
	r.s = r.s[n:]
	return n, nil
}

func statusOf(r *http.Response) any {
	if r == nil {
		return "nil"
	}
	return r.StatusCode
}

func freePort(t *testing.T) int {
	t.Helper()
	l, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("listen: %v", err)
	}
	defer func() { _ = l.Close() }()
	return l.Addr().(*net.TCPAddr).Port
}

func waitHealthz(t *testing.T, base string) {
	t.Helper()
	for i := 0; i < 50; i++ {
		resp, err := http.Get(base + "/healthz")
		if err == nil {
			_ = resp.Body.Close()
			if resp.StatusCode == http.StatusOK {
				return
			}
		}
		time.Sleep(100 * time.Millisecond)
	}
	t.Fatal("healthz not ready")
}
