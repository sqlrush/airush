//go:build integration

package main

import (
	"context"
	"fmt"
	"net"
	"net/http"
	"syscall"
	"testing"
	"time"

	"github.com/sqlrush/airush/libs/obs"
)

// TestRunServerLifecycle 启动→healthz→优雅退出全链路（此前仅由 obs-smoke 脚本
// 在 kind 环境验证；纳入集成测试使 runServer 进入合并覆盖口径）。
func TestRunServerLifecycle(t *testing.T) {
	port := freePort(t)
	cfg := appConfig{
		LogLevel: "info",
		Listen:   fmt.Sprintf("127.0.0.1:%d", port),
	}
	provider := obs.Init(context.Background(), obs.Config{
		Component: component, LogLevel: cfg.LogLevel, SampleRatio: 1.0,
	})

	done := make(chan error, 1)
	go func() { done <- runServer(cfg, provider, "test") }()

	base := "http://" + cfg.Listen
	waitHealthz(t, base)

	resp, err := http.Get(base + "/demo")
	if err != nil || resp.StatusCode != http.StatusOK {
		t.Fatalf("demo: %v status=%v", err, resp)
	}
	_ = resp.Body.Close()

	// SIGTERM → 优雅退出（signal.NotifyContext 捕获，进程不退）
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
