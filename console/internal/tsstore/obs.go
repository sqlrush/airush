package tsstore

import (
	"context"
	"errors"
	"log/slog"
	"time"

	"go.opentelemetry.io/otel/metric"

	"github.com/sqlrush/airush/libs/apierror"
	"github.com/sqlrush/airush/libs/obs"
)

// 采集落库的观测三件套（spec-0.9）。
//
// label 只用白名单里已有的 status/code：kind（metrics/slowlog/…）与 layer（raw/5m/1h）
// 虽然基数很低，但白名单扩充按 spec-0.9 §2.2 要修订那份已 frozen 的 spec，
// 为一个"锦上添花"的维度不值得。这两个维度进结构化日志，排障照样查得到。
var (
	writeBatches = obs.Counter("airush_console_timeseries_write_batches_total", "status", "code")
	writeRows    = obs.Counter("airush_console_timeseries_write_rows_total")
	queryLatency = obs.Histogram("airush_console_timeseries_query_duration_ms", "status", "code")
)

// observeWrite 记一次写入的成败与行数。行数只在成功时计——
// 失败是整事务回滚，记进去会让"写了多少行"这个数永远偏大。
func observeWrite(ctx context.Context, rows int, err error) error {
	if err != nil {
		writeBatches.Add(ctx, 1, metricAttrs("error", err))
		return err
	}
	writeBatches.Add(ctx, 1, metricAttrs("ok", nil))
	writeRows.Add(ctx, int64(rows))
	return nil
}

// observeQuery 记一次查询耗时。用 defer 调用，err 取命名返回值。
func observeQuery(ctx context.Context, start time.Time, err error) {
	queryLatency.Record(ctx, float64(time.Since(start).Microseconds())/1000,
		metricAttrs(statusOf(err), err))
}

func statusOf(err error) string {
	if err != nil {
		return "error"
	}
	return "ok"
}

// metricAttrs 组装 status(+code)。code 取 apierror 的稳定错误码；
// 非 apierror 的裸错误归为 unknown，避免把 err.Error() 当 label（无界基数）。
func metricAttrs(status string, err error) metric.MeasurementOption {
	if err == nil {
		return metric.WithAttributes(obs.Labels("status", status)...)
	}
	return metric.WithAttributes(obs.Labels("status", status, "code", codeOf(err))...)
}

func codeOf(err error) string {
	var ae *apierror.Error
	if errors.As(err, &ae) {
		return string(ae.Code)
	}
	return "unknown"
}

// logPolicies 启动时把超表的压缩/保留/连续聚合策略打进日志。
//
// 这些策略由迁移创建、由 TimescaleDB 后台作业执行，进程里看不见——一旦某次迁移
// 漏了 add_retention_policy，磁盘会安静地一直涨到爆，没有任何告警。启动时读一次
// timescaledb_information.jobs 把实际生效的策略打出来，是最便宜的一道核对。
func (s *Store) logPolicies(ctx context.Context, logger *slog.Logger) {
	if logger == nil {
		return
	}
	rows, err := s.pool.Query(ctx, `SELECT proc_name, hypertable_schema || '.' || hypertable_name,
			coalesce(config::text, '{}'), schedule_interval::text
		FROM timescaledb_information.jobs
		WHERE hypertable_schema IN ('tsdb', '_timescaledb_internal')
		ORDER BY proc_name`)
	if err != nil {
		// 读不到策略不该拦住启动：它是核对手段，不是运行依赖。
		logger.Warn("读取 TimescaleDB 策略失败（不影响服务，但请人工核对保留期是否生效）",
			"err", err)
		return
	}
	defer rows.Close()

	n := 0
	for rows.Next() {
		var proc, table, config, interval string
		if err := rows.Scan(&proc, &table, &config, &interval); err != nil {
			logger.Warn("解析 TimescaleDB 策略行失败", "err", err)
			return
		}
		n++
		logger.Info("timescaledb policy", "proc", proc, "hypertable", table,
			"config", config, "schedule_interval", interval)
	}
	if n == 0 {
		logger.Warn("未发现任何 TimescaleDB 策略作业——压缩与保留期可能都没生效，磁盘会一直涨")
	}
}
