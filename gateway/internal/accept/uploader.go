package accept

import (
	"context"
	"log/slog"

	"github.com/sqlrush/airush/libs/metrics"
)

// Uploader 是采集数据的上报出口（spec-1.5 D5）。
//
// 为什么不用 metrics.Sink：那个接口没有租户参数。数据全落一个内存 buffer 时
// 无所谓，一旦真落库，**gateway 是多租户中继**——同一进程同时中转多个租户的
// Connector 数据，租户是这个操作的一等参数。把它藏进 context 会让越权更难被看出来，
// 也让"这条数据属于谁"这个最要紧的问题在类型签名上看不见。
//
// 实现方是 consoleclient.Client（POST 给 console，console 落库）。gateway 自身
// 不持有 DB 连接——它面向客户侧 Connector，爆炸半径值得保住（spec-1.5 §8 Q5）。
type Uploader interface {
	UploadMetrics(ctx context.Context, tenantID, connectorID string, batch metrics.Batch) error
	UploadSnapshot(ctx context.Context, tenantID, connectorID string, snap metrics.Snapshot) error
}

// discardUploader 是无上报出口时的落点：**记日志**而非静默吞掉。
// 静默丢会让"采集正常但没数据"成为无声故障，那种问题最难查。
type discardUploader struct{ logger *slog.Logger }

func (d discardUploader) UploadMetrics(_ context.Context, tenantID, connectorID string, batch metrics.Batch) error {
	d.logger.Warn("采集数据无上报出口，已丢弃（未配置 CONSOLE_URL？）",
		"tenant_id", tenantID, "connector_id", connectorID, "datasource_id", batch.DatasourceID,
		"metrics", len(batch.Metrics))
	return nil
}

func (d discardUploader) UploadSnapshot(_ context.Context, tenantID, connectorID string, snap metrics.Snapshot) error {
	d.logger.Warn("快照无上报出口，已丢弃（未配置 CONSOLE_URL？）",
		"tenant_id", tenantID, "connector_id", connectorID, "datasource_id", snap.DatasourceID, "kind", snap.Kind)
	return nil
}
