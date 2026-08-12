package collector

import (
	"context"
	"errors"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/sqlrush/airush/console/internal/repo"
	"github.com/sqlrush/airush/libs/metrics"
)

func testLogger() *slog.Logger { return slog.New(slog.NewTextHandler(io.Discard, nil)) }

// fakeConnector 记录 TriggerCollect 调用并可注入错误（race-safe，loop 在 goroutine 调用）。
type fakeConnector struct {
	calls atomic.Int64
	mu    sync.Mutex
	last  [3]string
	err   error
}

func (f *fakeConnector) TriggerCollect(_ context.Context, connectorID, datasourceID, engineFamily string) error {
	f.calls.Add(1)
	f.mu.Lock()
	f.last = [3]string{connectorID, datasourceID, engineFamily}
	f.mu.Unlock()
	return f.err
}

func (f *fakeConnector) lastArgs() [3]string {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.last
}

func waitCond(t *testing.T, timeout time.Duration, cond func() bool) {
	t.Helper()
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		if cond() {
			return
		}
		time.Sleep(5 * time.Millisecond)
	}
	t.Fatal("condition not met within timeout")
}

func TestCollectOnceConnectorTriggers(t *testing.T) {
	t.Parallel()
	fc := &fakeConnector{}
	c := New(nil, nil, fc, metrics.NewBufferSink(4), DefaultConfig(), "tenant", testLogger())

	err := c.collectOnce(context.Background(), "ds1", target{mode: "connector", connectorID: "conn1", engineFamily: "postgres"})
	if err != nil {
		t.Fatalf("collectOnce connector = %v", err)
	}
	if fc.calls.Load() != 1 || fc.lastArgs() != [3]string{"conn1", "ds1", "postgres"} {
		t.Fatalf("trigger args = %d %v", fc.calls.Load(), fc.lastArgs())
	}
}

// TestLoopPeriodicSuccess 覆盖 loop 成功分支：周期触发 + 重置退避。
func TestLoopPeriodicSuccess(t *testing.T) {
	t.Parallel()
	fc := &fakeConnector{}
	c := New(nil, nil, fc, metrics.NewBufferSink(4),
		Config{Interval: 15 * time.Millisecond, MinInterval: 10 * time.Millisecond, Backoff: 5 * time.Millisecond},
		"tenant", testLogger())
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go c.loop(ctx, "ds1", target{mode: "connector", connectorID: "c1", engineFamily: "postgres"})
	waitCond(t, 2*time.Second, func() bool { return fc.calls.Load() >= 3 })
}

// TestLoopBackoffOnFailure 覆盖 loop 失败分支：采集失败退避后重试，不永久停摆。
func TestLoopBackoffOnFailure(t *testing.T) {
	t.Parallel()
	fc := &fakeConnector{err: errors.New("connector down")}
	c := New(nil, nil, fc, metrics.NewBufferSink(4),
		Config{Interval: 15 * time.Millisecond, MinInterval: 10 * time.Millisecond, Backoff: 5 * time.Millisecond},
		"tenant", testLogger())
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go c.loop(ctx, "ds1", target{mode: "connector", connectorID: "c1"})
	waitCond(t, 2*time.Second, func() bool { return fc.calls.Load() >= 2 })
}

func TestCollectOnceConnectorDisabled(t *testing.T) {
	t.Parallel()
	c := New(nil, nil, nil, metrics.NewBufferSink(4), DefaultConfig(), "tenant", testLogger())
	err := c.collectOnce(context.Background(), "ds1", target{mode: "connector", connectorID: "conn1"})
	if !errors.Is(err, ErrConnectorPathDisabled) {
		t.Fatalf("disabled path = %v, want ErrConnectorPathDisabled", err)
	}
}

func TestTargetFor(t *testing.T) {
	t.Parallel()
	cid := "conn-1"
	empty := ""
	cases := []struct {
		name   string
		ds     repo.Datasource
		wantOK bool
		want   target
	}{
		{
			"direct",
			repo.Datasource{ID: "d", EngineFamily: "postgres", ConnectMode: "direct"},
			true,
			target{engineFamily: "postgres", mode: "direct"},
		},
		{
			"connector",
			repo.Datasource{ID: "c", EngineFamily: "postgres", ConnectMode: "connector", ConnectorID: &cid},
			true,
			target{engineFamily: "postgres", mode: "connector", connectorID: "conn-1"},
		},
		{"connector-no-id", repo.Datasource{ConnectMode: "connector"}, false, target{}},
		{"connector-empty-id", repo.Datasource{ConnectMode: "connector", ConnectorID: &empty}, false, target{}},
		{"other-mode", repo.Datasource{ConnectMode: "unknown"}, false, target{}},
	}
	for _, tc := range cases {
		got, ok := targetFor(tc.ds)
		if ok != tc.wantOK || got != tc.want {
			t.Fatalf("%s: targetFor = %+v,%v want %+v,%v", tc.name, got, ok, tc.want, tc.wantOK)
		}
	}
}

func TestGatewayErrCode(t *testing.T) {
	t.Parallel()
	if got := gatewayErrCode([]byte(`{"code":"AR_CONNECTOR_OFFLINE"}`)); got != "AR_CONNECTOR_OFFLINE" {
		t.Fatalf("code from json = %q", got)
	}
	if got := gatewayErrCode([]byte(`plain text error`)); got != "plain text error" {
		t.Fatalf("fallback text = %q", got)
	}
	long := make([]byte, 300)
	for i := range long {
		long[i] = 'x'
	}
	if got := gatewayErrCode(long); len(got) != 200 {
		t.Fatalf("truncation len = %d, want 200", len(got))
	}
}

// TestStopAll 白盒：stopAll 取消并清空全部采集循环（确定性覆盖生命周期收尾）。
func TestStopAll(t *testing.T) {
	t.Parallel()
	c := New(nil, nil, nil, metrics.NewBufferSink(4), DefaultConfig(), "tenant", testLogger())
	stopped := 0
	for _, id := range []string{"a", "b", "c"} {
		c.loops[id] = func() { stopped++ }
	}
	c.stopAll()
	if len(c.loops) != 0 {
		t.Fatalf("loops not cleared: %d", len(c.loops))
	}
	if stopped != 3 {
		t.Fatalf("cancel called %d times, want 3", stopped)
	}
}

func TestConfigEffectiveIntervalFloor(t *testing.T) {
	t.Parallel()
	cfg := Config{Interval: 5 * time.Second, MinInterval: 15 * time.Second}
	if got := cfg.effectiveInterval(); got != 15*time.Second {
		t.Fatalf("effectiveInterval = %v, want 15s floor", got)
	}
}

func TestGatewayClientTriggerCollectOK(t *testing.T) {
	t.Parallel()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Authorization") != "Bearer tok" {
			w.WriteHeader(http.StatusUnauthorized)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"status":"ok"}`))
	}))
	defer srv.Close()

	c := NewGatewayClient(srv.URL, "tok")
	if err := c.TriggerCollect(context.Background(), "conn1", "ds1", "postgres"); err != nil {
		t.Fatalf("TriggerCollect ok = %v", err)
	}
}

func TestGatewayClientTriggerCollectError(t *testing.T) {
	t.Parallel()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusServiceUnavailable)
		_, _ = w.Write([]byte(`{"code":"AR_CONNECTOR_OFFLINE","message":"offline"}`))
	}))
	defer srv.Close()

	c := NewGatewayClient(srv.URL, "tok")
	err := c.TriggerCollect(context.Background(), "conn1", "ds1", "postgres")
	if err == nil {
		t.Fatal("expected error on 503")
	}
	if got := err.Error(); !contains(got, "AR_CONNECTOR_OFFLINE") {
		t.Fatalf("error = %q, want to mention AR_CONNECTOR_OFFLINE", got)
	}
}

func TestGatewayClientTriggerCollectConnError(t *testing.T) {
	t.Parallel()
	// 指向已关闭端点 → http.Do 失败（覆盖网络错误分支）。
	srv := httptest.NewServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) {}))
	url := srv.URL
	srv.Close()

	c := NewGatewayClient(url, "tok")
	if err := c.TriggerCollect(context.Background(), "conn1", "ds1", "postgres"); err == nil {
		t.Fatal("expected connection error to closed server")
	}
}

func contains(s, sub string) bool {
	for i := 0; i+len(sub) <= len(s); i++ {
		if s[i:i+len(sub)] == sub {
			return true
		}
	}
	return false
}
