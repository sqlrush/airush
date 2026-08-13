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
	return NewSessionServer(nil, DefaultSessionConfig(), sink, slog.New(slog.NewTextHandler(io.Discard, nil)))
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
		Metrics: []metrics.Metric{{Name: "pg.connections.active", Value: 3, Unit: metrics.UnitCount}},
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
		CommandId: "cmd1", Payload: []byte("not-json"),
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

func TestHandleDataUploadSinkFailureSignalsError(t *testing.T) {
	t.Parallel()
	s := testSessionServer(failSink{})
	sess := newSession("c1", "t1")
	sig := sess.awaitCommand("cmd1")

	batch := metrics.Batch{DatasourceID: "ds1", Metrics: []metrics.Metric{{Name: "m", Value: 1, Unit: metrics.UnitCount}}}
	payload, _ := json.Marshal(batch)
	s.handleDataUpload(context.Background(), sess, &connectorv1.DataUpload{CommandId: "cmd1", Payload: payload})

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
	cases := map[string]string{
		"AR_METRICS_COLLECT_FAILED: decode batch": "AR_METRICS_COLLECT_FAILED",
		"AR_CONNECTOR_OFFLINE":                    "AR_CONNECTOR_OFFLINE",
		"send command: rpc closed":                "AR_METRICS_COLLECT_FAILED",
	}
	for in, want := range cases {
		if got := dispatchErrCode(errString(in)); got != want {
			t.Fatalf("dispatchErrCode(%q) = %q, want %q", in, got, want)
		}
	}
}

type errString string

func (e errString) Error() string { return string(e) }
