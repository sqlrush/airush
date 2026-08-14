package svcapi

import (
	"net/http"

	"github.com/sqlrush/airush/console/internal/tenancy"
	"github.com/sqlrush/airush/libs/apierror"
	"github.com/sqlrush/airush/libs/metrics"
)

// 采集数据上报入口（spec-1.5 D5，§8 Q5 选项 A）。
//
// 为什么 gateway 不自己直连库：
//   ① gateway 是面向客户侧 Connector 的接入组件，给它 DB 访问会显著扩大被攻破后的
//      爆炸半径——它今天连一个 DB 连接都没有，这个属性值得保住；
//   ② console 已有租户上下文中间件与事务基座（SET LOCAL app.tenant_id），
//      复用比在 gateway 重建一套正确性高得多；
//   ③ 吞吐无压力：1000 个数据源 60s 采集 ≈ 17 req/s。
//
// 代价是多一跳，且 console 成为写入路径的单点（可水平扩容缓解）。

// ingestMetricsRequest 是 gateway 转发的指标批。
// tenant_id 由 gateway 从 Connector 的 mTLS 证书 SAN 解析后带上——
// 与 enroll/handshake 同一信任基础。
type ingestMetricsRequest struct {
	TenantID string        `json:"tenant_id"`
	Batch    metrics.Batch `json:"batch"`
}

// ingestSnapshotRequest 是 gateway 转发的快照。
type ingestSnapshotRequest struct {
	TenantID string           `json:"tenant_id"`
	Snapshot metrics.Snapshot `json:"snapshot"`
}

func (s *Server) ingestMetrics(w http.ResponseWriter, r *http.Request) error {
	var req ingestMetricsRequest
	if err := decodeJSON(r, &req); err != nil {
		return err
	}
	if req.TenantID == "" || req.Batch.DatasourceID == "" {
		return apierror.New(apierror.CodeValidationFailed).WithDetails(
			apierror.Detail{Field: "tenant_id/batch.datasource_id", Reason: "必填"})
	}
	if s.sink == nil {
		// 规则 6：未配置落点时显式拒绝，不假装收下（否则采集侧会以为数据已存）。
		return apierror.New(apierror.CodeCommonNotImplemented)
	}
	ctx := tenancy.WithTenant(r.Context(), req.TenantID)
	if err := s.sink.Publish(ctx, req.Batch); err != nil {
		return err
	}
	w.WriteHeader(http.StatusAccepted)
	return nil
}

func (s *Server) ingestSnapshot(w http.ResponseWriter, r *http.Request) error {
	var req ingestSnapshotRequest
	if err := decodeJSON(r, &req); err != nil {
		return err
	}
	if req.TenantID == "" || req.Snapshot.DatasourceID == "" {
		return apierror.New(apierror.CodeValidationFailed).WithDetails(
			apierror.Detail{Field: "tenant_id/snapshot.datasource_id", Reason: "必填"})
	}
	// kind 白名单在此处也校验一次（AD-9 双侧校验；tsstore 侧还有一道）。
	if !metrics.ValidSnapshotKind(req.Snapshot.Kind) {
		return apierror.New(apierror.CodeCollectUnsupportedKind)
	}
	if s.snapshotSink == nil {
		return apierror.New(apierror.CodeCommonNotImplemented)
	}
	ctx := tenancy.WithTenant(r.Context(), req.TenantID)
	if err := s.snapshotSink.PublishSnapshot(ctx, req.Snapshot); err != nil {
		return err
	}
	w.WriteHeader(http.StatusAccepted)
	return nil
}
