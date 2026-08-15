package httpapi

import (
	"context"
	"net/http"
	"strconv"
	"time"

	"github.com/sqlrush/airush/console/internal/tsstore"
	"github.com/sqlrush/airush/libs/apierror"
	"github.com/sqlrush/airush/libs/metrics"
)

// CollectedReader 是采集数据的只读查询面（tsstore.Store 满足）。
// 接口化而非直接吃 *tsstore.Store：httpapi 只需要读，写路径由 collector 与 svcapi 走，
// 小接口让"这里绝不会写"在类型上就成立。
type CollectedReader interface {
	SeriesRange(ctx context.Context, datasourceID, seriesName, entityID string,
		from, to time.Time, step time.Duration) ([]tsstore.Point, error)
	TopEntities(ctx context.Context, datasourceID, seriesName string,
		from, to time.Time, n int) ([]tsstore.RankedEntity, error)
	LatestSnapshot(ctx context.Context, datasourceID, kind string) (*tsstore.SnapshotWithPayload, error)
	SnapshotHistory(ctx context.Context, datasourceID, kind string, limit int) ([]tsstore.SnapshotMeta, error)
}

// 查询窗口与步长的护栏。窗口无上限会让一次请求扫穿整个保留期；
// 步长过小会让一次响应回上万个点，两者都是拒绝而非静默截断（规则 6）。
const (
	maxQueryWindow = 400 * 24 * time.Hour // = 1h 层保留期，再长也没有数据
	minQueryStep   = time.Second
	maxQueryPoints = 5000
	defaultTopN    = 10
	maxTopN        = 200
)

// seriesRange GET /api/v1/datasources/{id}/series?name=&entity=&from=&to=&step=
func (s *Server) seriesRange(w http.ResponseWriter, r *http.Request) error {
	dsID, err := pathUUID(r, "id")
	if err != nil {
		return err
	}
	if s.collected == nil {
		return apierror.New(apierror.CodeCommonNotImplemented)
	}
	q := r.URL.Query()
	name := q.Get("name")
	if _, ok := metrics.LookupSeries(name); !ok {
		// 未声明的 series 直接拒——既是输入校验，也让"目录里有什么"成为唯一事实源。
		return apierror.New(apierror.CodeValidationFailed).WithDetails(
			apierror.Detail{Field: "name", Reason: "未知的 series 名"})
	}
	from, to, err := parseWindow(q.Get("from"), q.Get("to"))
	if err != nil {
		return err
	}
	step, err := parseStep(q.Get("step"), from, to)
	if err != nil {
		return err
	}

	points, err := s.collected.SeriesRange(r.Context(), dsID, name, q.Get("entity"), from, to, step)
	if err != nil {
		return err
	}
	if points == nil {
		points = []tsstore.Point{}
	}
	return writeJSON(w, http.StatusOK, map[string]any{
		"datasource_id": dsID, "name": name, "entity": q.Get("entity"),
		"from": from, "to": to, "step": step.String(), "points": points,
	})
}

// topEntities GET /api/v1/datasources/{id}/top-entities?name=&from=&to=&limit=
func (s *Server) topEntities(w http.ResponseWriter, r *http.Request) error {
	dsID, err := pathUUID(r, "id")
	if err != nil {
		return err
	}
	if s.collected == nil {
		return apierror.New(apierror.CodeCommonNotImplemented)
	}
	q := r.URL.Query()
	name := q.Get("name")
	decl, ok := metrics.LookupSeries(name)
	if !ok || decl.EntityKind == "" {
		// 无实体维度的 series 排 Top N 无意义——显式拒绝，别返回空数组让调用方猜。
		return apierror.New(apierror.CodeValidationFailed).WithDetails(
			apierror.Detail{Field: "name", Reason: "该 series 不带实体维度，无法排名"})
	}
	from, to, err := parseWindow(q.Get("from"), q.Get("to"))
	if err != nil {
		return err
	}
	limit, err := parseLimit(q.Get("limit"), defaultTopN, maxTopN)
	if err != nil {
		return err
	}

	items, err := s.collected.TopEntities(r.Context(), dsID, name, from, to, limit)
	if err != nil {
		return err
	}
	if items == nil {
		items = []tsstore.RankedEntity{}
	}
	return writeJSON(w, http.StatusOK, map[string]any{
		"datasource_id": dsID, "name": name, "from": from, "to": to, "items": items,
	})
}

// latestSnapshot GET /api/v1/datasources/{id}/snapshots/{kind}
func (s *Server) latestSnapshot(w http.ResponseWriter, r *http.Request) error {
	dsID, kind, err := snapshotTarget(r)
	if err != nil {
		return err
	}
	if s.collected == nil {
		return apierror.New(apierror.CodeCommonNotImplemented)
	}
	snap, err := s.collected.LatestSnapshot(r.Context(), dsID, kind)
	if err != nil {
		return err
	}
	if snap == nil {
		// 还没采到 ≠ 出错，但对调用方是"资源不存在"。
		return apierror.New(apierror.CodeCommonNotFound)
	}
	return writeJSON(w, http.StatusOK, snap)
}

// snapshotHistory GET /api/v1/datasources/{id}/snapshots/{kind}/history?limit=
func (s *Server) snapshotHistory(w http.ResponseWriter, r *http.Request) error {
	dsID, kind, err := snapshotTarget(r)
	if err != nil {
		return err
	}
	if s.collected == nil {
		return apierror.New(apierror.CodeCommonNotImplemented)
	}
	limit, err := parseLimit(r.URL.Query().Get("limit"), 20, 200)
	if err != nil {
		return err
	}
	items, err := s.collected.SnapshotHistory(r.Context(), dsID, kind, limit)
	if err != nil {
		return err
	}
	if items == nil {
		items = []tsstore.SnapshotMeta{}
	}
	return writeJSON(w, http.StatusOK, map[string]any{"items": items})
}

// snapshotTarget 取并校验 (数据源, kind)。slowlog 不入快照表（它走读数流水），
// 故这里只认 schema/config——传 slowlog 拒绝而不是返回空，避免调用方误以为"没采到"。
func snapshotTarget(r *http.Request) (string, string, error) {
	dsID, err := pathUUID(r, "id")
	if err != nil {
		return "", "", err
	}
	kind := r.PathValue("kind")
	if kind != metrics.SnapshotKindSchema && kind != metrics.SnapshotKindConfig {
		return "", "", apierror.New(apierror.CodeCollectUnsupportedKind).WithDetails(
			apierror.Detail{Field: "kind", Reason: "快照查询只支持 schema / config；慢查询走 series 面"})
	}
	return dsID, kind, nil
}

// parseWindow 解析时间窗；缺省为最近 1 小时。
func parseWindow(fromStr, toStr string) (time.Time, time.Time, error) {
	now := time.Now().UTC()
	to := now
	if toStr != "" {
		v, err := time.Parse(time.RFC3339, toStr)
		if err != nil {
			return time.Time{}, time.Time{}, badParam("to", "需要 RFC3339 时间")
		}
		to = v
	}
	from := to.Add(-time.Hour)
	if fromStr != "" {
		v, err := time.Parse(time.RFC3339, fromStr)
		if err != nil {
			return time.Time{}, time.Time{}, badParam("from", "需要 RFC3339 时间")
		}
		from = v
	}
	if !from.Before(to) {
		return time.Time{}, time.Time{}, badParam("from", "必须早于 to")
	}
	if to.Sub(from) > maxQueryWindow {
		return time.Time{}, time.Time{}, badParam("from", "查询窗口超过 400 天（最粗一层的保留期）")
	}
	return from, to, nil
}

// parseStep 解析步长；缺省按窗口切 200 个点，并拒绝会产生过多点的组合。
func parseStep(stepStr string, from, to time.Time) (time.Duration, error) {
	if stepStr == "" {
		return maxDuration(to.Sub(from)/200, minQueryStep), nil
	}
	step, err := time.ParseDuration(stepStr)
	if err != nil {
		return 0, badParam("step", "需要 Go duration 形态，如 5m / 1h")
	}
	if step < minQueryStep {
		return 0, badParam("step", "不得小于 1s")
	}
	if int64(to.Sub(from)/step) > maxQueryPoints {
		return 0, badParam("step", "该窗口下点数超过 5000，请增大 step 或缩小窗口")
	}
	return step, nil
}

func parseLimit(s string, def, max int) (int, error) {
	if s == "" {
		return def, nil
	}
	n, err := strconv.Atoi(s)
	if err != nil || n <= 0 {
		return 0, badParam("limit", "需要正整数")
	}
	if n > max {
		return 0, badParam("limit", "超过上限 "+strconv.Itoa(max))
	}
	return n, nil
}

func badParam(field, reason string) error {
	return apierror.New(apierror.CodeValidationFailed).WithDetails(
		apierror.Detail{Field: field, Reason: reason})
}

func maxDuration(a, b time.Duration) time.Duration {
	if a > b {
		return a
	}
	return b
}
