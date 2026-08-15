package httpapi

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/sqlrush/airush/console/internal/tsstore"
	"github.com/sqlrush/airush/libs/apierror"
)

// 采集查询面的 handler 层用测试替身验，不起容器：它要证的是路由、参数校验与
// "落点缺席时显式 501"，与 TimescaleDB 的行为无关（那部分在 tsstore 集成用例里）。

const (
	testTenantID = "00000000-0000-0000-0000-000000000001"
	testDSID     = "aaaaaaaa-0000-0000-0000-00000000000a"
)

// fakeCollected 是 CollectedReader 的替身，可注入返回值与错误。
type fakeCollected struct {
	points   []tsstore.Point
	entities []tsstore.RankedEntity
	snapshot *tsstore.SnapshotWithPayload
	history  []tsstore.SnapshotMeta
	err      error
}

func (f *fakeCollected) SeriesRange(_ context.Context, _, _, _ string,
	_, _ time.Time, _ time.Duration,
) ([]tsstore.Point, error) {
	return f.points, f.err
}

func (f *fakeCollected) TopEntities(_ context.Context, _, _ string,
	_, _ time.Time, _ int,
) ([]tsstore.RankedEntity, error) {
	return f.entities, f.err
}

func (f *fakeCollected) LatestSnapshot(_ context.Context, _, _ string) (*tsstore.SnapshotWithPayload, error) {
	return f.snapshot, f.err
}

func (f *fakeCollected) SnapshotHistory(_ context.Context, _, _ string, _ int) ([]tsstore.SnapshotMeta, error) {
	return f.history, f.err
}

// newTestHandler 构造只装采集查询面的 Server（store/sealer 为 nil——这些路由不碰库）。
func newTestHandler(t *testing.T, reader CollectedReader) http.Handler {
	t.Helper()
	s, err := New(nil, nil, nil, testTenantID)
	if err != nil {
		t.Fatalf("new server: %v", err)
	}
	if reader != nil {
		s = s.WithCollected(reader)
	}
	return s.Handler()
}

func do(t *testing.T, h http.Handler, path string) *httptest.ResponseRecorder {
	t.Helper()
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, path, nil))
	return rec
}

// wantErrCode 断言错误响应里的稳定错误码（不比对文案——文案会变，错误码是契约）。
func wantErrCode(t *testing.T, rec *httptest.ResponseRecorder, status int, code apierror.Code) {
	t.Helper()
	if rec.Code != status {
		t.Fatalf("http status = %d, want %d（body: %s）", rec.Code, status, rec.Body.String())
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

func TestSeriesRangeHandler(t *testing.T) {
	now := time.Now().UTC()
	reader := &fakeCollected{points: []tsstore.Point{
		{At: now, Avg: 8, Min: 7, Max: 9, Last: 9, Samples: 2},
	}}
	h := newTestHandler(t, reader)

	t.Run("正常返回点", func(t *testing.T) {
		rec := do(t, h, "/api/v1/datasources/"+testDSID+"/series?name=db.connections.active&step=5m")
		if rec.Code != http.StatusOK {
			t.Fatalf("status = %d: %s", rec.Code, rec.Body.String())
		}
		var body struct {
			Name   string          `json:"name"`
			Points []tsstore.Point `json:"points"`
		}
		if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
			t.Fatalf("decode: %v", err)
		}
		if body.Name != "db.connections.active" || len(body.Points) != 1 {
			t.Fatalf("body = %+v", body)
		}
	})

	t.Run("未声明的series被拒", func(t *testing.T) {
		// 目录是唯一事实源：查不存在的 series 必须报错，不能回空数组让调用方
		// 误以为"这段时间没数据"。
		rec := do(t, h, "/api/v1/datasources/"+testDSID+"/series?name=db.made.up")
		wantErrCode(t, rec, http.StatusBadRequest, apierror.CodeValidationFailed)
	})

	t.Run("非UUID数据源被拒", func(t *testing.T) {
		rec := do(t, h, "/api/v1/datasources/not-a-uuid/series?name=db.connections.active")
		wantErrCode(t, rec, http.StatusBadRequest, apierror.CodeValidationFailed)
	})

	t.Run("窗口越界被拒", func(t *testing.T) {
		from := now.Add(-maxQueryWindow - time.Hour).Format(time.RFC3339)
		rec := do(t, h, "/api/v1/datasources/"+testDSID+"/series?name=db.connections.active&from="+from)
		wantErrCode(t, rec, http.StatusBadRequest, apierror.CodeValidationFailed)
	})

	t.Run("空结果返回空数组而非null", func(t *testing.T) {
		// null 会让前端图表组件炸在"不是数组"上；这里显式归一。
		rec := do(t, newTestHandler(t, &fakeCollected{}),
			"/api/v1/datasources/"+testDSID+"/series?name=db.connections.active")
		if rec.Code != http.StatusOK {
			t.Fatalf("status = %d", rec.Code)
		}
		if !strings.Contains(rec.Body.String(), `"points":[]`) {
			t.Fatalf("body 未把空结果归一成 []: %s", rec.Body.String())
		}
	})

	t.Run("存储报错原样冒泡", func(t *testing.T) {
		bad := &fakeCollected{err: apierror.New(apierror.CodeTimeseriesQueryFailed)}
		rec := do(t, newTestHandler(t, bad),
			"/api/v1/datasources/"+testDSID+"/series?name=db.connections.active")
		wantErrCode(t, rec, http.StatusInternalServerError, apierror.CodeTimeseriesQueryFailed)
	})

	t.Run("未配置落点时显式501", func(t *testing.T) {
		rec := do(t, newTestHandler(t, nil),
			"/api/v1/datasources/"+testDSID+"/series?name=db.connections.active")
		wantErrCode(t, rec, http.StatusNotImplemented, apierror.CodeCommonNotImplemented)
	})
}

func TestTopEntitiesHandler(t *testing.T) {
	reader := &fakeCollected{entities: []tsstore.RankedEntity{
		{EntityID: "e1", Label: "SELECT 1", Total: 5},
	}}
	h := newTestHandler(t, reader)

	t.Run("正常返回排名", func(t *testing.T) {
		rec := do(t, h, "/api/v1/datasources/"+testDSID+"/top-entities?name=db.slowlog.total_seconds&limit=5")
		if rec.Code != http.StatusOK {
			t.Fatalf("status = %d: %s", rec.Code, rec.Body.String())
		}
	})

	t.Run("无实体维度的series被拒", func(t *testing.T) {
		// 对没有实体维度的指标排 Top N 是问错了问题——显式拒绝而不是回空数组。
		rec := do(t, h, "/api/v1/datasources/"+testDSID+"/top-entities?name=db.connections.active")
		wantErrCode(t, rec, http.StatusBadRequest, apierror.CodeValidationFailed)
	})

	t.Run("limit越界被拒", func(t *testing.T) {
		rec := do(t, h, "/api/v1/datasources/"+testDSID+"/top-entities?name=db.slowlog.total_seconds&limit=9999")
		wantErrCode(t, rec, http.StatusBadRequest, apierror.CodeValidationFailed)
	})

	t.Run("未配置落点时显式501", func(t *testing.T) {
		rec := do(t, newTestHandler(t, nil),
			"/api/v1/datasources/"+testDSID+"/top-entities?name=db.slowlog.total_seconds")
		wantErrCode(t, rec, http.StatusNotImplemented, apierror.CodeCommonNotImplemented)
	})
}

func TestSnapshotHandlers(t *testing.T) {
	reader := &fakeCollected{
		snapshot: &tsstore.SnapshotWithPayload{
			SnapshotMeta: tsstore.SnapshotMeta{ID: "s1", Kind: "schema"},
		},
		history: []tsstore.SnapshotMeta{{ID: "s1", Kind: "schema"}},
	}
	h := newTestHandler(t, reader)

	t.Run("取当前版本", func(t *testing.T) {
		rec := do(t, h, "/api/v1/datasources/"+testDSID+"/snapshots/schema")
		if rec.Code != http.StatusOK {
			t.Fatalf("status = %d: %s", rec.Code, rec.Body.String())
		}
	})

	t.Run("版本链", func(t *testing.T) {
		rec := do(t, h, "/api/v1/datasources/"+testDSID+"/snapshots/schema/history?limit=5")
		if rec.Code != http.StatusOK {
			t.Fatalf("status = %d: %s", rec.Code, rec.Body.String())
		}
	})

	t.Run("还没采到时404", func(t *testing.T) {
		// tsstore 返回 (nil, nil) 表示"还没采到"——对 HTTP 调用方那是资源不存在。
		rec := do(t, newTestHandler(t, &fakeCollected{}),
			"/api/v1/datasources/"+testDSID+"/snapshots/schema")
		wantErrCode(t, rec, http.StatusNotFound, apierror.CodeCommonNotFound)
	})

	t.Run("slowlog不走快照面", func(t *testing.T) {
		rec := do(t, h, "/api/v1/datasources/"+testDSID+"/snapshots/slowlog")
		wantErrCode(t, rec, http.StatusBadRequest, apierror.CodeCollectUnsupportedKind)
	})

	t.Run("未配置落点时显式501", func(t *testing.T) {
		nilH := newTestHandler(t, nil)
		wantErrCode(t, do(t, nilH, "/api/v1/datasources/"+testDSID+"/snapshots/schema"),
			http.StatusNotImplemented, apierror.CodeCommonNotImplemented)
		wantErrCode(t, do(t, nilH, "/api/v1/datasources/"+testDSID+"/snapshots/schema/history"),
			http.StatusNotImplemented, apierror.CodeCommonNotImplemented)
	})

	t.Run("历史查询limit非法被拒", func(t *testing.T) {
		rec := do(t, h, "/api/v1/datasources/"+testDSID+"/snapshots/schema/history?limit=abc")
		wantErrCode(t, rec, http.StatusBadRequest, apierror.CodeValidationFailed)
	})

	t.Run("存储报错原样冒泡", func(t *testing.T) {
		bad := &fakeCollected{err: apierror.New(apierror.CodeTimeseriesQueryFailed)}
		badH := newTestHandler(t, bad)
		wantErrCode(t, do(t, badH, "/api/v1/datasources/"+testDSID+"/snapshots/schema"),
			http.StatusInternalServerError, apierror.CodeTimeseriesQueryFailed)
		wantErrCode(t, do(t, badH, "/api/v1/datasources/"+testDSID+"/snapshots/schema/history"),
			http.StatusInternalServerError, apierror.CodeTimeseriesQueryFailed)
	})
}

// TestTopEntitiesStorageError 单列：TopEntities 的错误路径与 SeriesRange 不共用分支。
func TestTopEntitiesStorageError(t *testing.T) {
	bad := &fakeCollected{err: apierror.New(apierror.CodeTimeseriesQueryFailed)}
	rec := do(t, newTestHandler(t, bad),
		"/api/v1/datasources/"+testDSID+"/top-entities?name=db.slowlog.total_seconds")
	wantErrCode(t, rec, http.StatusInternalServerError, apierror.CodeTimeseriesQueryFailed)
}
