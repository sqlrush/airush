package accept

import (
	"context"
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/sqlrush/airush/libs/metrics"
	connectorv1 "github.com/sqlrush/airush/proto/gen/go/connector/v1"
)

func testSessionServer(sink metrics.Sink) *SessionServer {
	snapshotSink, _ := sink.(metrics.SnapshotSink) // BufferSink 兼任两者；failSink 则为 nil→discard
	return NewSessionServer(nil, DefaultSessionConfig(), sink, snapshotSink,
		slog.New(slog.NewTextHandler(io.Discard, nil)))
}

// testSessionServerWithSnapshots 显式注入快照落点（用于快照失败路径）。
func testSessionServerWithSnapshots(sink metrics.Sink, snapshots metrics.SnapshotSink) *SessionServer {
	return NewSessionServer(nil, DefaultSessionConfig(), sink, snapshots,
		slog.New(slog.NewTextHandler(io.Discard, nil)))
}

func collectReqBody(t *testing.T) string {
	t.Helper()
	b, err := json.Marshal(collectRequest{ConnectorID: "c1", DatasourceID: "ds1", EngineFamily: "postgres"})
	if err != nil {
		t.Fatal(err)
	}
	return string(b)
}

func TestCollectHandlerAuth(t *testing.T) {
	t.Parallel()
	h := CollectHandler(&Servers{sessionSvc: testSessionServer(metrics.NewBufferSink(4))}, "secret")

	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodPost, "/internal/v1/collect", strings.NewReader(collectReqBody(t)))
	req.Header.Set("Authorization", "Bearer wrong")
	h.ServeHTTP(rec, req)
	if rec.Code != http.StatusUnauthorized {
		t.Fatalf("auth reject = %d, want 401", rec.Code)
	}
}

func TestCollectHandlerValidation(t *testing.T) {
	t.Parallel()
	h := CollectHandler(&Servers{sessionSvc: testSessionServer(metrics.NewBufferSink(4))}, "secret")

	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodPost, "/internal/v1/collect", strings.NewReader(`{"connector_id":""}`))
	req.Header.Set("Authorization", "Bearer secret")
	h.ServeHTTP(rec, req)
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("validation = %d, want 400", rec.Code)
	}
}

func TestCollectHandlerConnectorOffline(t *testing.T) {
	t.Parallel()
	h := CollectHandler(&Servers{sessionSvc: testSessionServer(metrics.NewBufferSink(4))}, "secret")

	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodPost, "/internal/v1/collect", strings.NewReader(collectReqBody(t)))
	req.Header.Set("Authorization", "Bearer secret")
	h.ServeHTTP(rec, req)
	if rec.Code != http.StatusServiceUnavailable {
		t.Fatalf("offline = %d, want 503", rec.Code)
	}
	var body map[string]string
	_ = json.Unmarshal(rec.Body.Bytes(), &body)
	if body["code"] != "AR_CONNECTOR_OFFLINE" {
		t.Fatalf("offline code = %q", body["code"])
	}
}

func TestHandleDataUploadPublishesAndSignals(t *testing.T) {
	t.Parallel()
	sink := metrics.NewBufferSink(4)
	s := testSessionServer(sink)
	sess := newSession("c1", "t1")
	sig := sess.awaitCommand("cmd1")

	batch := metrics.Batch{
		DatasourceID: "ds1", EngineFamily: "postgres", CatalogVersion: metrics.CatalogVersion,
		Metrics: []metrics.Metric{{Name: "db.connections.active", Value: 3, Unit: metrics.UnitCount}},
	}
	payload, _ := json.Marshal(batch)
	s.handleDataUpload(context.Background(), sess, &connectorv1.DataUpload{
		CommandId: "cmd1", DatasourceId: "ds1", Kind: "metrics", Payload: payload,
	})

	if sink.Total() != 1 {
		t.Fatalf("sink total = %d, want 1", sink.Total())
	}
	got, ok := sink.Latest()
	if !ok || got.DatasourceID != "ds1" || len(got.Metrics) != 1 {
		t.Fatalf("latest batch = %+v ok=%v", got, ok)
	}
	if err := <-sig; err != nil {
		t.Fatalf("waiter signalled error: %v", err)
	}
}

func TestHandleDataUploadBadPayloadFailsClosed(t *testing.T) {
	t.Parallel()
	sink := metrics.NewBufferSink(4)
	s := testSessionServer(sink)
	sess := newSession("c1", "t1")
	sig := sess.awaitCommand("cmd1")

	s.handleDataUpload(context.Background(), sess, &connectorv1.DataUpload{
		CommandId: "cmd1", Kind: "metrics", Payload: []byte("not-json"),
	})
	if sink.Total() != 0 {
		t.Fatalf("bad payload published to sink: total=%d", sink.Total())
	}
	if err := <-sig; err == nil {
		t.Fatal("bad payload should signal error")
	}
}

// failSink 让 Publish 恒失败（验证 handleDataUpload 落 Sink 失败 fail-closed）。
type failSink struct{}

func (failSink) Publish(context.Context, metrics.Batch) error { return errString("boom") }

func (failSink) PublishSnapshot(context.Context, metrics.Snapshot) error { return errString("boom") }

func TestHandleDataUploadSinkFailureSignalsError(t *testing.T) {
	t.Parallel()
	s := testSessionServer(failSink{})
	sess := newSession("c1", "t1")
	sig := sess.awaitCommand("cmd1")

	batch := metrics.Batch{DatasourceID: "ds1", Metrics: []metrics.Metric{{Name: "m", Value: 1, Unit: metrics.UnitCount}}}
	payload, _ := json.Marshal(batch)
	s.handleDataUpload(context.Background(), sess, &connectorv1.DataUpload{
		CommandId: "cmd1", Kind: "metrics", Payload: payload,
	})

	if err := <-sig; err == nil {
		t.Fatal("sink publish failure should signal error")
	}
}

func TestCommandResultErr(t *testing.T) {
	t.Parallel()
	if err := commandResultErr(&connectorv1.CommandResult{Status: connectorv1.CommandResult_STATUS_OK}); err != nil {
		t.Fatalf("OK → %v, want nil", err)
	}
	err := commandResultErr(&connectorv1.CommandResult{
		Status: connectorv1.CommandResult_STATUS_ERROR,
		Error:  &connectorv1.CommandError{Code: "AR_METRICS_COLLECT_FAILED"},
	})
	if err == nil || err.Error() != "AR_METRICS_COLLECT_FAILED" {
		t.Fatalf("error map = %v", err)
	}
	// 无码兜底
	if err := commandResultErr(&connectorv1.CommandResult{Status: connectorv1.CommandResult_STATUS_UNSUPPORTED}); err == nil ||
		err.Error() != "AR_INTERNAL_ERROR" {
		t.Fatalf("no-code fallback = %v", err)
	}
}

func TestDispatchErrCode(t *testing.T) {
	t.Parallel()
	cases := []struct {
		in, kind, want string
	}{
		{"AR_METRICS_COLLECT_FAILED: decode batch", "metrics", "AR_METRICS_COLLECT_FAILED"},
		{"AR_CONNECTOR_OFFLINE", "metrics", "AR_CONNECTOR_OFFLINE"},
		{"send command: rpc closed", "metrics", "AR_METRICS_COLLECT_FAILED"},
		// 无法从错误里取码时按采集类型归位：快照类归快照失败码。
		{"send command: rpc closed", metrics.SnapshotKindSlowlog, "AR_SNAPSHOT_COLLECT_FAILED"},
		{"AR_SNAPSHOT_COLLECT_FAILED: sink publish", metrics.SnapshotKindSchema, "AR_SNAPSHOT_COLLECT_FAILED"},
	}
	for _, tc := range cases {
		if got := dispatchErrCode(errString(tc.in), tc.kind); got != tc.want {
			t.Fatalf("dispatchErrCode(%q, %q) = %q, want %q", tc.in, tc.kind, got, tc.want)
		}
	}
}

// TestCollectHandlerUnknownKind：未知采集类型在平台入口就被拒（AD-9 双侧拒绝的平台侧）。
func TestCollectHandlerUnknownKind(t *testing.T) {
	t.Parallel()
	h := CollectHandler(&Servers{sessionSvc: testSessionServer(metrics.NewBufferSink(4))}, "secret")

	body, err := json.Marshal(collectRequest{
		ConnectorID: "c1", DatasourceID: "ds1", EngineFamily: "postgres", Kind: "rowdump",
	})
	if err != nil {
		t.Fatal(err)
	}
	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodPost, "/internal/v1/collect", strings.NewReader(string(body)))
	req.Header.Set("Authorization", "Bearer secret")
	h.ServeHTTP(rec, req)

	if rec.Code != http.StatusBadRequest {
		t.Fatalf("unknown kind = %d, want 400", rec.Code)
	}
	if !strings.Contains(rec.Body.String(), "AR_COLLECT_UNSUPPORTED_KIND") {
		t.Fatalf("body = %s", rec.Body.String())
	}
}

// TestHandleSnapshotUpload：快照落 SnapshotSink 并关联回触发方；帧头 kind 与载荷
// 自述 kind 不一致时拒收（防错落 Sink）。
func TestHandleSnapshotUpload(t *testing.T) {
	t.Parallel()
	sink := metrics.NewBufferSink(4)
	s := testSessionServer(sink)
	sess := newSession("c1", "t1")

	snapshot := metrics.Snapshot{
		DatasourceID: "ds1", Kind: metrics.SnapshotKindConfig,
		Configs: []metrics.ConfigEntry{{Name: "max_connections", Value: "100"}},
	}
	payload, _ := json.Marshal(snapshot)

	sig := sess.awaitCommand("cmd1")
	s.handleDataUpload(context.Background(), sess, &connectorv1.DataUpload{
		CommandId: "cmd1", DatasourceId: "ds1", Kind: metrics.SnapshotKindConfig, Payload: payload,
	})
	if err := <-sig; err != nil {
		t.Fatalf("waiter signalled error: %v", err)
	}
	if sink.SnapshotTotal() != 1 {
		t.Fatalf("snapshot total = %d, want 1", sink.SnapshotTotal())
	}
	got, ok := sink.LatestSnapshotOf(metrics.SnapshotKindConfig)
	if !ok || len(got.Configs) != 1 {
		t.Fatalf("latest snapshot = %+v ok=%v", got, ok)
	}
	if sink.Total() != 0 {
		t.Fatal("a snapshot must not land in the metrics sink")
	}

	// kind 不一致 → 拒收
	sig = sess.awaitCommand("cmd2")
	s.handleDataUpload(context.Background(), sess, &connectorv1.DataUpload{
		CommandId: "cmd2", Kind: metrics.SnapshotKindSchema, Payload: payload,
	})
	if err := <-sig; err == nil {
		t.Fatal("kind mismatch should signal an error")
	}
	if sink.SnapshotTotal() != 1 {
		t.Fatalf("mismatched snapshot published: total=%d", sink.SnapshotTotal())
	}
}

// TestHandleDataUploadUnknownKind：未知 kind 的上报被拒（连接器侧越权兜底）。
func TestHandleDataUploadUnknownKind(t *testing.T) {
	t.Parallel()
	sink := metrics.NewBufferSink(4)
	s := testSessionServer(sink)
	sess := newSession("c1", "t1")
	sig := sess.awaitCommand("cmd1")

	s.handleDataUpload(context.Background(), sess, &connectorv1.DataUpload{
		CommandId: "cmd1", Kind: "rowdump", Payload: []byte(`{}`),
	})
	err := <-sig
	if err == nil || !strings.Contains(err.Error(), "AR_COLLECT_UNSUPPORTED_KIND") {
		t.Fatalf("unknown kind signal = %v", err)
	}
	if sink.Total() != 0 || sink.SnapshotTotal() != 0 {
		t.Fatal("nothing should be published for an unknown kind")
	}
}

// TestHandleSnapshotUploadSinkFailure：快照落 Sink 失败 fail-closed。
func TestHandleSnapshotUploadSinkFailure(t *testing.T) {
	t.Parallel()
	s := testSessionServerWithSnapshots(metrics.NewBufferSink(4), failSink{})
	sess := newSession("c1", "t1")
	sig := sess.awaitCommand("cmd1")

	payload, _ := json.Marshal(metrics.Snapshot{Kind: metrics.SnapshotKindSlowlog})
	s.handleDataUpload(context.Background(), sess, &connectorv1.DataUpload{
		CommandId: "cmd1", Kind: metrics.SnapshotKindSlowlog, Payload: payload,
	})
	if err := <-sig; err == nil {
		t.Fatal("snapshot sink failure should signal error")
	}
}

type errString string

func (e errString) Error() string { return string(e) }
