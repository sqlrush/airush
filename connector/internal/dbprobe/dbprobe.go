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
	"errors"
	"fmt"
	"strconv"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/sqlrush/airush/libs/metrics"
	connectorv1 "github.com/sqlrush/airush/proto/gen/go/connector/v1"
)

// 采集指令类型（平台下发）。每个 kind 一个独立指令类型即白名单本身——未知类型
// 在此显式拒绝，指令 payload 里**没有 SQL**，SQL 全部来自编译期目录（AD-9）。
const (
	CommandProbeMetrics = "PROBE_METRICS" // spec-1.3
	CommandProbeSlowlog = "PROBE_SLOWLOG" // spec-1.4
	CommandProbeSchema  = "PROBE_SCHEMA"
	CommandProbeConfig  = "PROBE_CONFIG"
)

// DataUploadKindMetrics 是指标类上报的 kind；快照类 kind 用 metrics.SnapshotKind*。
const DataUploadKindMetrics = "metrics"

// snapshotKindForCommand 把快照类指令映射到快照 kind；非快照指令返回 ok=false。
func snapshotKindForCommand(commandType string) (string, bool) {
	switch commandType {
	case CommandProbeSlowlog:
		return metrics.SnapshotKindSlowlog, true
	case CommandProbeSchema:
		return metrics.SnapshotKindSchema, true
	case CommandProbeConfig:
		return metrics.SnapshotKindConfig, true
	default:
		return "", false
	}
}

// probeRequest 是采集指令的 payload——只有目标标识，绝无 SQL（AD-9）。
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
	switch {
	case cmd.GetType() == CommandProbeMetrics:
		return h.handleMetrics(ctx, cmd)
	case isSnapshotCommand(cmd.GetType()):
		return h.handleSnapshot(ctx, cmd)
	default:
		return nil
	}
}

func isSnapshotCommand(commandType string) bool {
	_, ok := snapshotKindForCommand(commandType)
	return ok
}

func (h *Handler) handleMetrics(ctx context.Context, cmd *connectorv1.Command) *connectorv1.ClientFrame {
	req, bad := decodeProbeRequest(cmd)
	if bad != nil {
		return bad
	}
	probe := metrics.Probe{DatasourceID: req.DatasourceID, EngineFamily: req.EngineFamily}
	batch, err := probe.Collect(ctx, &querier{pool: h.pool})
	if err != nil {
		return errFrame(cmd.GetCommandId(), "AR_METRICS_COLLECT_FAILED", "指标采集失败")
	}
	return uploadFrame(cmd, req.DatasourceID, DataUploadKindMetrics, batch)
}

// handleSnapshot 处理三类快照指令（spec-1.4）。采集失败回 CommandResult(error)；
// 能力缺失是**成功路径**，照常上报 CapabilityMissing 快照供上层提示开启。
func (h *Handler) handleSnapshot(ctx context.Context, cmd *connectorv1.Command) *connectorv1.ClientFrame {
	kind, ok := snapshotKindForCommand(cmd.GetType())
	if !ok {
		return errFrame(cmd.GetCommandId(), "AR_COLLECT_UNSUPPORTED_KIND", "不支持的采集类型")
	}
	req, bad := decodeProbeRequest(cmd)
	if bad != nil {
		return bad
	}
	probe := metrics.SnapshotProbe{DatasourceID: req.DatasourceID, EngineFamily: req.EngineFamily}
	snapshot, err := probe.Collect(ctx, &querier{pool: h.pool}, kind)
	if err != nil {
		if errors.Is(err, metrics.ErrUnsupportedKind) {
			return errFrame(cmd.GetCommandId(), "AR_COLLECT_UNSUPPORTED_KIND", "不支持的采集类型")
		}
		return errFrame(cmd.GetCommandId(), "AR_SNAPSHOT_COLLECT_FAILED", "快照采集失败")
	}
	return uploadFrame(cmd, req.DatasourceID, kind, snapshot)
}

func decodeProbeRequest(cmd *connectorv1.Command) (probeRequest, *connectorv1.ClientFrame) {
	var req probeRequest
	if err := json.Unmarshal(cmd.GetPayload(), &req); err != nil {
		return req, errFrame(cmd.GetCommandId(), "AR_VALIDATION_FAILED", cmd.GetType()+" payload 非法")
	}
	return req, nil
}

// uploadFrame 把采集产物 JSON 化装进 DataUpload 帧（spec-1.3 §2.4 的通道约定）。
func uploadFrame(cmd *connectorv1.Command, datasourceID, kind string, payload any) *connectorv1.ClientFrame {
	body, err := json.Marshal(payload)
	if err != nil {
		return errFrame(cmd.GetCommandId(), "AR_INTERNAL_ERROR", "采集结果序列化失败")
	}
	return &connectorv1.ClientFrame{Frame: &connectorv1.ClientFrame_DataUpload{
		DataUpload: &connectorv1.DataUpload{
			CommandId:    cmd.GetCommandId(),
			DatasourceId: datasourceID,
			Kind:         kind,
			Payload:      body,
		},
	}}
}

// querier 适配连接器 pgx 池到通道无关探针接口（与 directconn 同构，探针代码共享）。
type querier struct{ pool *pgxpool.Pool }

var (
	_ metrics.Querier    = (*querier)(nil)
	_ metrics.RowQuerier = (*querier)(nil)
)

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

// QueryRows 只读执行一条快照目录 SQL，按 列名 → 字符串值 逐行返回（NULL 为空串），
// 达到 maxRows 即停。与 directconn 侧同语义——快照探针跨通道共享。
func (q *querier) QueryRows(ctx context.Context, sql string, maxRows int) ([]map[string]string, error) {
	rows, err := q.pool.Query(ctx, sql)
	if err != nil {
		return nil, fmt.Errorf("dbprobe: query rows: %w", err)
	}
	defer rows.Close()

	fields := rows.FieldDescriptions()
	names := make([]string, len(fields))
	for i, f := range fields {
		names[i] = string(f.Name)
	}

	out := make([]map[string]string, 0, 16)
	for rows.Next() {
		if maxRows > 0 && len(out) >= maxRows {
			break
		}
		values, err := rows.Values()
		if err != nil {
			return nil, fmt.Errorf("dbprobe: scan row: %w", err)
		}
		row := make(map[string]string, len(names))
		for i, name := range names {
			if i < len(values) {
				row[name] = stringifyValue(values[i])
			}
		}
		out = append(out, row)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("dbprobe: read rows: %w", err)
	}
	return out, nil
}

// stringifyValue 把 pgx 解出的任意标量转成字符串；NULL → 空串。
func stringifyValue(v any) string {
	switch value := v.(type) {
	case nil:
		return ""
	case string:
		return value
	case []byte:
		return string(value)
	case bool:
		if value {
			return "true"
		}
		return "false"
	case time.Time:
		return value.UTC().Format(time.RFC3339Nano)
	case float64:
		return strconv.FormatFloat(value, 'f', -1, 64)
	case float32:
		return strconv.FormatFloat(float64(value), 'f', -1, 32)
	case int64:
		return strconv.FormatInt(value, 10)
	case int32:
		return strconv.FormatInt(int64(value), 10)
	case int:
		return strconv.Itoa(value)
	default:
		return fmt.Sprint(value)
	}
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
