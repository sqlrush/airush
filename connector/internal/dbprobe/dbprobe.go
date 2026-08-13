// Package dbprobe 是连接器侧的指标探针执行（spec-1.3 D4 Connector 通道）。
// 连接器在客户网络内、对客户 DB 建立本地连接（凭据在客户侧，AD-4 边界不变），
// 收到平台下发的 PROBE_METRICS 指令时用共享 libs/metrics 探针采集、回传结构化 batch。
//
// Stage 1 简化（记录）：连接器配置单一 DB 目标（AIRUSH_CONNECTOR_DB_URL）——
// 一连接器管一库；多库/多目标映射列 Stage 2。
package dbprobe

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"

	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/sqlrush/airush/libs/metrics"
	connectorv1 "github.com/sqlrush/airush/proto/gen/go/connector/v1"
)

// CommandProbeMetrics 是采集指令类型（平台下发，spec-1.3 §2.2）。
const CommandProbeMetrics = "PROBE_METRICS"

// DataUploadKindMetrics 是指标类上报的 kind（spec-1.4 起扩展 slowlog/schema 等）。
const DataUploadKindMetrics = "metrics"

// probeRequest 是 PROBE_METRICS 指令 payload。
type probeRequest struct {
	DatasourceID string `json:"datasource_id"`
	EngineFamily string `json:"engine_family"`
}

// Handler 在连接器本地 DB 上执行探针（连接懒建、只读）。
type Handler struct {
	pool *pgxpool.Pool
}

// New 建连接器侧 DB 连接池（客户网络内直连客户库）。
func New(ctx context.Context, dbURL string) (*Handler, error) {
	pool, err := pgxpool.New(ctx, dbURL)
	if err != nil {
		return nil, fmt.Errorf("dbprobe: new pool: %w", err)
	}
	return &Handler{pool: pool}, nil
}

// Close 释放连接池。
func (h *Handler) Close() {
	if h.pool != nil {
		h.pool.Close()
	}
}

// Handle 处理 PROBE_METRICS 指令：运行探针、把 batch JSON 装进 DataUpload 帧回发
// （spec-1.3 §2.4：Connector 通道走 DataUpload → gateway 转 Sink）。采集/序列化失败
// 回 CommandResult(error)。其余指令类型返回 nil（交由链上其他 handler 兜底）。
func (h *Handler) Handle(ctx context.Context, cmd *connectorv1.Command) *connectorv1.ClientFrame {
	if cmd.GetType() != CommandProbeMetrics {
		return nil
	}
	var req probeRequest
	if err := json.Unmarshal(cmd.GetPayload(), &req); err != nil {
		return errFrame(cmd.GetCommandId(), "AR_VALIDATION_FAILED", "PROBE_METRICS payload 非法")
	}

	probe := metrics.Probe{DatasourceID: req.DatasourceID, EngineFamily: req.EngineFamily}
	batch, err := probe.Collect(ctx, &querier{pool: h.pool})
	if err != nil {
		return errFrame(cmd.GetCommandId(), "AR_METRICS_COLLECT_FAILED", "指标采集失败")
	}
	payload, err := json.Marshal(batch)
	if err != nil {
		return errFrame(cmd.GetCommandId(), "AR_INTERNAL_ERROR", "batch 序列化失败")
	}
	return &connectorv1.ClientFrame{Frame: &connectorv1.ClientFrame_DataUpload{
		DataUpload: &connectorv1.DataUpload{
			CommandId:    cmd.GetCommandId(),
			DatasourceId: req.DatasourceID,
			Kind:         DataUploadKindMetrics,
			Payload:      payload,
		},
	}}
}

// querier 适配连接器 pgx 池到通道无关探针接口（与 directconn 同构，探针代码共享）。
type querier struct{ pool *pgxpool.Pool }

var _ metrics.Querier = (*querier)(nil)

func (q *querier) QueryMetricValue(ctx context.Context, sql string) (float64, bool, error) {
	var v nullFloat
	if err := q.pool.QueryRow(ctx, sql).Scan(&v); err != nil {
		return 0, false, fmt.Errorf("dbprobe: query: %w", err)
	}
	if !v.Valid {
		return 0, false, nil
	}
	return v.Float64, true, nil
}

type nullFloat = sql.NullFloat64

// errFrame 把采集失败包成 CommandResult(error) 回发帧（spec-1.3 §2.4：失败走 CommandResult，
// 成功走 DataUpload）。gateway 侧据 command_id 关联回触发方并映射错误码。
func errFrame(id, code, msg string) *connectorv1.ClientFrame {
	return &connectorv1.ClientFrame{Frame: &connectorv1.ClientFrame_CommandResult{
		CommandResult: &connectorv1.CommandResult{
			CommandId: id,
			Status:    connectorv1.CommandResult_STATUS_ERROR,
			Error:     &connectorv1.CommandError{Code: code, Message: msg},
		},
	}}
}
