package runtime

import (
	"context"
	"time"

	"github.com/sqlrush/codexgo/pkg/protocol"
	"github.com/sqlrush/codexgo/pkg/rollout"

	"github.com/sqlrush/airush/agent-runtime/internal/pgstore"
)

// Drain 是 preStop 排水（spec-1.8 §3.7 / D5）：停领取新 turn → 等在飞 turn 跑完（≤ timeout）
// → 超时的 turn 中断、线程标 interrupted 并写 turn_aborted 事件 → 返回。rollout 是 SSOT，
// 被中断的线程可由任何 pod ResumeThread。
func (e *Engine) Drain(ctx context.Context, timeout time.Duration) {
	e.mu.Lock()
	e.draining = true
	snapshot := make([]*liveThread, 0, len(e.live))
	for _, lt := range e.live {
		snapshot = append(snapshot, lt)
	}
	e.mu.Unlock()
	e.logger.Info("draining", "live_threads", len(snapshot), "timeout", timeout)

	deadline := time.NewTimer(timeout)
	defer deadline.Stop()
	for _, lt := range snapshot {
		select {
		case <-lt.released:
			continue
		case <-deadline.C:
			e.interruptForDrain(ctx, lt)
			// 剩余的一并中断，不再等
			for _, rest := range snapshot {
				select {
				case <-rest.released:
				default:
					e.interruptForDrain(ctx, rest)
				}
			}
			return
		case <-ctx.Done():
			return
		}
	}
}

// interruptForDrain 中断一条在飞线程：先让 core 自己中止（它会写 turn_aborted 并由 pump 以
// interrupted 释放）；core 在宽限期内没反应才由 runtime 补写 turn_aborted 并强制释放。
func (e *Engine) interruptForDrain(ctx context.Context, lt *liveThread) {
	turn := lt.turnID()
	if lt.turnRunning() {
		_, _ = lt.codex.Submit(protocol.Op{Type: protocol.OpInterrupt})
		select {
		case <-lt.released:
			return
		case <-time.After(drainAbortGrace):
		case <-ctx.Done():
		}
		completed := e.now().Unix()
		reason := protocol.TurnAbortReasonInterrupted
		item := rollout.NewEventMsgItem(protocol.EventMsg{
			Type:        protocol.EventMsgKindTurnAborted,
			TurnAborted: &protocol.TurnAbortedEvent{TurnID: &turn, Reason: reason, CompletedAt: &completed},
		})
		if _, err := e.store.AppendRolloutItems(lt.ctx, lt.id, []rollout.RolloutItem{item}); err != nil {
			e.logger.Warn("record drain abort failed", "thread_id", lt.id.String(), "error", err)
		}
	}
	e.release(lt, pgstore.ThreadStatusInterrupted)
}

// drainAbortGrace 是排水时等 core 自行中止的宽限。
const drainAbortGrace = 3 * time.Second

// Recover 是启动期恢复（spec-1.8 §3.8）：把心跳过期的 running 线程标 interrupted。
// staleAfter 缺省 2× 心跳。返回命中数。
func (e *Engine) Recover(ctx context.Context) (int, error) {
	hits, err := e.store.MarkStaleRunningInterrupted(ctx, 2*e.cfg.HeartbeatInterval)
	if err != nil {
		return 0, err
	}
	for _, h := range hits {
		e.logger.Info("recovered orphan thread", "tenant_id", h.TenantID, "thread_id", h.ThreadID.String())
	}
	return len(hits), nil
}
