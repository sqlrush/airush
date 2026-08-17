package runtime

import (
	"context"
	"fmt"
	"time"

	"go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/metric"

	"github.com/sqlrush/airush/libs/obs"
)

// spec-1.8 T19：agent 运行时观测（spec-0.9 三件套的 metrics 面）。label 只用白名单里的
// status（completed / aborted / failed；有界），租户/线程/turn 进日志字段不进 label。
var (
	turnsTotal     = obs.Counter("airush_agent_turns_total", "status")
	turnDuration   = obs.Histogram("airush_agent_turn_duration_ms", "status")
	turnsInFlight  = mustUpDown("airush_agent_turns_in_flight")
	approvalsTotal = obs.Counter("airush_agent_approvals_total", "status")
)

// mustUpDown 建一个 in-flight gauge（UpDownCounter；无 label）。
func mustUpDown(name string) metric.Int64UpDownCounter {
	c, err := otel.Meter("airush").Int64UpDownCounter(name)
	if err != nil {
		panic(fmt.Sprintf("create updown counter %s: %v", name, err))
	}
	return c
}

// turn 结束状态（metrics label 值）。
const (
	turnStatusCompleted = "completed"
	turnStatusAborted   = "aborted"
	turnStatusFailed    = "failed"
)

// observeTurnStart / observeTurnEnd 记 turn 生命周期指标。
func observeTurnStart(ctx context.Context) {
	turnsInFlight.Add(ctx, 1)
}

func observeTurnEnd(ctx context.Context, status string, started time.Time) {
	turnsInFlight.Add(ctx, -1)
	attrs := metric.WithAttributes(obs.Labels("status", status)...)
	turnsTotal.Add(ctx, 1, attrs)
	if !started.IsZero() {
		turnDuration.Record(ctx, float64(time.Since(started).Microseconds())/1000, attrs)
	}
}

// observeApproval 记审批阶段结论（allowed / denied）。
func observeApproval(ctx context.Context, allowed bool) {
	status := "denied"
	if allowed {
		status = "allowed"
	}
	approvalsTotal.Add(ctx, 1, metric.WithAttributes(obs.Labels("status", status)...))
}
