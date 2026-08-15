package svcapi

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/sqlrush/airush/console/internal/tenancy"
	"github.com/sqlrush/airush/libs/apierror"
	"github.com/sqlrush/airush/libs/metrics"
)

// 上报入口是 Connector 通道数据进入平台的**唯一**门。它的三件事各自都能独立出错，
// 因而各自都要有用例：① 载荷校验；② 落点缺席时显式 501（不能假装收下——
// 采集侧会以为数据已存）；③ 落库时租户上下文必须来自载荷里的 tenant_id。

const (
	svcAuthForTest = "svc-auth-for-test"
	testTenantID   = "00000000-0000-0000-0000-000000000001"
	testDSID       = "aaaaaaaa-0000-0000-0000-00000000000a"
	testConnID     = "cccccccc-0000-0000-0000-00000000000c"
)

// recordingSink 记录落点收到的批与当时的租户上下文。
type recordingSink struct {
	mu        sync.Mutex
	batches   []metrics.Batch
	snapshots []metrics.Snapshot
	tenants   []string
	err       error
}

func (r *recordingSink) Publish(ctx context.Context, b metrics.Batch) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	tid, _ := tenancy.FromContext(ctx)
	r.tenants = append(r.tenants, tid)
	r.batches = append(r.batches, b)
	return r.err
}

func (r *recordingSink) PublishSnapshot(ctx context.Context, s metrics.Snapshot) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	tid, _ := tenancy.FromContext(ctx)
	r.tenants = append(r.tenants, tid)
	r.snapshots = append(r.snapshots, s)
	return r.err
}

func (r *recordingSink) lastTenant() string {
	r.mu.Lock()
	defer r.mu.Unlock()
	if len(r.tenants) == 0 {
		return ""
	}
	return r.tenants[len(r.tenants)-1]
}

// fakeOwnership 只认 (testDSID, testConnID) 这一对；其他组合按生产语义返回错误码。
type fakeOwnership struct{ calls int }

func (f *fakeOwnership) Check(_ context.Context, datasourceID, connectorID string) error {
	f.calls++
	if datasourceID != testDSID {
		return apierror.New(apierror.CodeDatasourceNotFound)
	}
	if connectorID != testConnID {
		return apierror.New(apierror.CodeCollectDatasourceMismatch)
	}
	return nil
}

// newIngestSrv 构造只带归属替身的 Server（store/ca 传 nil——上报路径不用它们）。
func newIngestSrv() *Server {
	return New(nil, nil, svcAuthForTest).WithOwnership(&fakeOwnership{})
}

// post 带 svc token 发一次请求。
func post(t *testing.T, srv *Server, path, body string) *httptest.ResponseRecorder {
	t.Helper()
	req := httptest.NewRequest(http.MethodPost, path, strings.NewReader(body))
	req.Header.Set("Authorization", "Bearer "+svcAuthForTest)
	rec := httptest.NewRecorder()
	srv.Handler().ServeHTTP(rec, req)
	return rec
}

func wantCode(t *testing.T, rec *httptest.ResponseRecorder, status int, code apierror.Code) {
	t.Helper()
	if rec.Code != status {
		t.Fatalf("status = %d, want %d（body: %s）", rec.Code, status, rec.Body.String())
	}
	var body apierror.Response
	if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
		t.Fatalf("响应不是错误信封: %s", rec.Body.String())
	}
	if body.Code != code {
		t.Fatalf("error code = %q, want %q", body.Code, code)
	}
	if body.TraceID == "" {
		t.Fatal("错误响应缺 trace_id（spec-0.8 §2.2 要求必达）")
	}
}

func metricsBody(tenantID, dsID string) string { return metricsBodyFor(tenantID, testConnID, dsID) }

func metricsBodyFor(tenantID, connID, dsID string) string {
	req := ingestMetricsRequest{
		TenantID: tenantID, ConnectorID: connID,
		Batch: metrics.Batch{
			DatasourceID: dsID, EngineFamily: "postgres",
			CatalogVersion: metrics.CatalogVersion, CollectedAt: time.Now().UTC(),
			Metrics: []metrics.Metric{{
				Name: "db.connections.active", Value: 7,
				Unit: metrics.UnitCount, At: time.Now().UTC(),
			}},
		},
	}
	buf, _ := json.Marshal(req)
	return string(buf)
}

func snapshotBody(tenantID, dsID, kind string) string {
	req := ingestSnapshotRequest{
		TenantID: tenantID, ConnectorID: testConnID,
		Snapshot: metrics.Snapshot{
			DatasourceID: dsID, EngineFamily: "postgres", Kind: kind,
			CatalogVersion: metrics.CatalogVersion, CollectedAt: time.Now().UTC(),
			Source: "pg_catalog",
		},
	}
	buf, _ := json.Marshal(req)
	return string(buf)
}

func TestIngestMetrics(t *testing.T) {
	t.Run("正常收讫并带上租户上下文", func(t *testing.T) {
		sink := &recordingSink{}
		srv := newIngestSrv().WithSinks(sink, sink)
		rec := post(t, srv, "/internal/v1/collected/metrics", metricsBody(testTenantID, testDSID))
		if rec.Code != http.StatusAccepted {
			t.Fatalf("status = %d: %s", rec.Code, rec.Body.String())
		}
		if len(sink.batches) != 1 || sink.batches[0].DatasourceID != testDSID {
			t.Fatalf("落点收到 %+v", sink.batches)
		}
		// 租户来自载荷（gateway 从 mTLS 证书 SAN 解析），不是默认值也不是空。
		if got := sink.lastTenant(); got != testTenantID {
			t.Fatalf("落点看到的租户 = %q, want %q", got, testTenantID)
		}
	})

	t.Run("缺租户或数据源被拒", func(t *testing.T) {
		sink := &recordingSink{}
		srv := newIngestSrv().WithSinks(sink, sink)
		wantCode(t, post(t, srv, "/internal/v1/collected/metrics", metricsBody("", testDSID)),
			http.StatusBadRequest, apierror.CodeValidationFailed)
		wantCode(t, post(t, srv, "/internal/v1/collected/metrics", metricsBody(testTenantID, "")),
			http.StatusBadRequest, apierror.CodeValidationFailed)
		if len(sink.batches) != 0 {
			t.Fatal("校验未通过的载荷仍然落到了 Sink")
		}
	})

	t.Run("坏JSON被拒", func(t *testing.T) {
		srv := newIngestSrv().WithSinks(&recordingSink{}, &recordingSink{})
		wantCode(t, post(t, srv, "/internal/v1/collected/metrics", `{"tenant_id":`),
			http.StatusBadRequest, apierror.CodeValidationFailed)
	})

	t.Run("数据源不属于该连接器被拒", func(t *testing.T) {
		// 租户内的数据源归属：连接器自报的 datasource_id 必须是它名下的。
		// 被攻破的连接器不得给同租户其他数据源灌数据。
		sink := &recordingSink{}
		srv := newIngestSrv().WithSinks(sink, sink)
		wantCode(t, post(t, srv, "/internal/v1/collected/metrics",
			metricsBodyFor(testTenantID, "dddddddd-0000-0000-0000-00000000000d", testDSID)),
			http.StatusForbidden, apierror.CodeCollectDatasourceMismatch)
		// 数据源在本租户视图内查无 → 404（fail-closed 的另一面）
		wantCode(t, post(t, srv, "/internal/v1/collected/metrics",
			metricsBody(testTenantID, "eeeeeeee-0000-0000-0000-00000000000e")),
			http.StatusNotFound, apierror.CodeDatasourceNotFound)
		if len(sink.batches) != 0 {
			t.Fatal("归属校验未通过的载荷仍然落到了 Sink")
		}
	})

	t.Run("缺connector_id被拒", func(t *testing.T) {
		srv := newIngestSrv().WithSinks(&recordingSink{}, &recordingSink{})
		wantCode(t, post(t, srv, "/internal/v1/collected/metrics",
			metricsBodyFor(testTenantID, "", testDSID)),
			http.StatusBadRequest, apierror.CodeValidationFailed)
	})

	t.Run("无归属校验器时显式501", func(t *testing.T) {
		// 没有归属校验就收数据 = 放弃这道防线；宁可 501。
		srv := New(nil, nil, svcAuthForTest).WithSinks(&recordingSink{}, &recordingSink{})
		wantCode(t, post(t, srv, "/internal/v1/collected/metrics", metricsBody(testTenantID, testDSID)),
			http.StatusNotImplemented, apierror.CodeCommonNotImplemented)
	})

	t.Run("未配置落点时显式501", func(t *testing.T) {
		// 规则 6：不能返回 202 假装收下——那样采集侧会认为数据已入库。
		srv := newIngestSrv() // 有归属校验、无落点
		wantCode(t, post(t, srv, "/internal/v1/collected/metrics", metricsBody(testTenantID, testDSID)),
			http.StatusNotImplemented, apierror.CodeCommonNotImplemented)
	})

	t.Run("落库失败原样冒泡", func(t *testing.T) {
		sink := &recordingSink{err: apierror.New(apierror.CodeTimeseriesWriteFailed)}
		srv := newIngestSrv().WithSinks(sink, sink)
		wantCode(t, post(t, srv, "/internal/v1/collected/metrics", metricsBody(testTenantID, testDSID)),
			http.StatusInternalServerError, apierror.CodeTimeseriesWriteFailed)
	})

	t.Run("无svc token被拒", func(t *testing.T) {
		srv := newIngestSrv().WithSinks(&recordingSink{}, &recordingSink{})
		req := httptest.NewRequest(http.MethodPost, "/internal/v1/collected/metrics",
			strings.NewReader(metricsBody(testTenantID, testDSID)))
		rec := httptest.NewRecorder()
		srv.Handler().ServeHTTP(rec, req)
		wantCode(t, rec, http.StatusUnauthorized, apierror.CodeSvcUnauthenticated)
	})
}

func TestIngestSnapshot(t *testing.T) {
	t.Run("三类合法kind都收", func(t *testing.T) {
		for _, kind := range []string{
			metrics.SnapshotKindSlowlog, metrics.SnapshotKindSchema, metrics.SnapshotKindConfig,
		} {
			sink := &recordingSink{}
			srv := newIngestSrv().WithSinks(sink, sink)
			rec := post(t, srv, "/internal/v1/collected/snapshots",
				snapshotBody(testTenantID, testDSID, kind))
			if rec.Code != http.StatusAccepted {
				t.Fatalf("kind=%s status = %d: %s", kind, rec.Code, rec.Body.String())
			}
			if sink.lastTenant() != testTenantID {
				t.Fatalf("kind=%s 租户上下文 = %q", kind, sink.lastTenant())
			}
		}
	})

	t.Run("未知kind在此处也拒一次", func(t *testing.T) {
		// AD-9 双侧校验：tsstore 侧还有一道，但入口先拒能少一次无谓的事务。
		sink := &recordingSink{}
		srv := newIngestSrv().WithSinks(sink, sink)
		wantCode(t, post(t, srv, "/internal/v1/collected/snapshots",
			snapshotBody(testTenantID, testDSID, "bogus")),
			http.StatusBadRequest, apierror.CodeCollectUnsupportedKind)
		if len(sink.snapshots) != 0 {
			t.Fatal("未知 kind 的快照仍然落到了 Sink")
		}
	})

	t.Run("缺租户或数据源被拒", func(t *testing.T) {
		srv := newIngestSrv().WithSinks(&recordingSink{}, &recordingSink{})
		wantCode(t, post(t, srv, "/internal/v1/collected/snapshots",
			snapshotBody("", testDSID, metrics.SnapshotKindSchema)),
			http.StatusBadRequest, apierror.CodeValidationFailed)
	})

	t.Run("坏JSON被拒", func(t *testing.T) {
		srv := newIngestSrv().WithSinks(&recordingSink{}, &recordingSink{})
		wantCode(t, post(t, srv, "/internal/v1/collected/snapshots", `{`),
			http.StatusBadRequest, apierror.CodeValidationFailed)
	})

	t.Run("未配置落点时显式501", func(t *testing.T) {
		srv := newIngestSrv() // 有归属校验、无落点
		wantCode(t, post(t, srv, "/internal/v1/collected/snapshots",
			snapshotBody(testTenantID, testDSID, metrics.SnapshotKindSchema)),
			http.StatusNotImplemented, apierror.CodeCommonNotImplemented)
	})

	t.Run("落库失败原样冒泡", func(t *testing.T) {
		sink := &recordingSink{err: apierror.New(apierror.CodeTimeseriesWriteFailed)}
		srv := newIngestSrv().WithSinks(sink, sink)
		wantCode(t, post(t, srv, "/internal/v1/collected/snapshots",
			snapshotBody(testTenantID, testDSID, metrics.SnapshotKindSchema)),
			http.StatusInternalServerError, apierror.CodeTimeseriesWriteFailed)
	})
}
