package runtime

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"

	"github.com/sqlrush/codexgo/pkg/core"
	"github.com/sqlrush/codexgo/pkg/protocol"
	"github.com/sqlrush/codexgo/pkg/threadstore"

	"github.com/sqlrush/airush/agent-runtime/internal/pgstore"
	"github.com/sqlrush/airush/libs/apierror"
	"github.com/sqlrush/airush/libs/obs"
)

// Limiter 是每租户并发 turn 上限（spec-1.8 §3.5：Stage 1 = 单 pod 配置值；配额中心化在 1.7）。
type Limiter interface {
	TryAcquire(tenantID string) bool
	Release(tenantID string)
}

// SetLimiter 注入并发上限（nil = 不限）。
func (e *Engine) SetLimiter(l Limiter) { e.limiterT = l }

// StartThread 建线程（不发 turn）：线程行 + 属性（agent / model / title）。
func (e *Engine) StartThread(ctx context.Context, in StartThreadInput) (ThreadRef, error) {
	if _, err := tenantOf(ctx); err != nil {
		return ThreadRef{}, err
	}
	model := in.Model
	var agentID *string
	if in.AgentID != "" {
		agent, err := e.store.GetAgent(ctx, in.AgentID)
		if err != nil {
			return ThreadRef{}, err
		}
		agentID = &agent.ID
		if model == "" {
			model = agent.DefaultModel
		}
	}
	if model == "" {
		model = e.cfg.DefaultModel
	}
	threadID := protocol.NewThreadIDV7()
	err := e.store.Threads().CreateThread(ctx, threadstore.CreateThreadParams{
		SessionID:   threadID.ToSessionID(),
		ThreadID:    threadID,
		Source:      airushSessionSource(),
		HistoryMode: protocol.ThreadHistoryModePaginated,
		Metadata:    threadstore.ThreadPersistenceMetadata{ModelProvider: providerName},
	})
	if err != nil {
		return ThreadRef{}, err
	}
	if err := e.store.SetThreadAttributes(ctx, threadID, pgstore.ThreadAttributes{AgentID: agentID, Model: model, Title: in.Title}); err != nil {
		return ThreadRef{}, err
	}
	return ThreadRef{ThreadID: threadID.String()}, nil
}

// SubmitTurn 发起 / steer 一轮：
//   - 线程在本 pod 运行中 → steer（core InputQueue 中断当前 wait 并接纳，0.147 语义），Queued=true；
//   - 线程空闲 → 领取（ClaimTurn）→ 租户并发额度 → 启动会话 → 提交；额度不足则入队等 sweeper；
//   - 线程被别的 pod 持有 → 只入队（steer），由持有方的 dispatchPending 接纳。
//
// 输入先落队列（耐久）再提交，接纳后写 admitted_turn_id：进程死在中间 = 至少一次重投，不丢。
func (e *Engine) SubmitTurn(ctx context.Context, threadID string, in TurnInput) (TurnRef, error) {
	if len(in.Items) == 0 {
		return TurnRef{}, apierror.New(apierror.CodeValidationFailed)
	}
	tid := protocol.NewThreadID(threadID)
	info, err := e.store.GetThreadInfo(ctx, tid)
	if err != nil {
		return TurnRef{}, err
	}
	if info.Status == pgstore.ThreadStatusArchived {
		return TurnRef{}, apierror.New(apierror.CodeAgentThreadNotFound)
	}
	if _, err := e.enqueue(ctx, tid, queuedPayload{Type: queuedUserInput, Items: in.Items}, e.kindFor(tid)); err != nil {
		return TurnRef{}, err
	}
	turnID, admitted, err := e.dispatch(ctx, info)
	if err != nil {
		return TurnRef{}, err
	}
	if !admitted {
		return TurnRef{Queued: true}, nil
	}
	return TurnRef{TurnID: turnID, Queued: e.isSteer(tid, turnID)}, nil
}

// kindFor：线程在本 pod 上跑着 → steer，否则 queued。
func (e *Engine) kindFor(tid protocol.ThreadID) pgstore.QueueKind {
	if lt := e.liveFor(tid); lt != nil && lt.turnRunning() {
		return pgstore.QueueKindSteer
	}
	return pgstore.QueueKindQueued
}

func (e *Engine) isSteer(tid protocol.ThreadID, turnID string) bool {
	lt := e.liveFor(tid)
	return lt != nil && lt.turnID() == turnID && lt.steered
}

func (e *Engine) liveFor(tid protocol.ThreadID) *liveThread {
	e.mu.Lock()
	defer e.mu.Unlock()
	return e.live[tid.String()]
}

// enqueue 把输入写进外置队列，返回队列 id。
func (e *Engine) enqueue(ctx context.Context, tid protocol.ThreadID, p queuedPayload, kind pgstore.QueueKind) (string, error) {
	raw, err := json.Marshal(p)
	if err != nil {
		return "", apierror.Wrap(apierror.CodeValidationFailed, err)
	}
	id := protocol.NewUUIDV7()
	if err := e.store.EnqueueInput(ctx, tid, id, kind, raw); err != nil {
		return "", err
	}
	return id, nil
}

// dispatch 尽力把线程队列里的待接纳输入交给 core：返回 (最后接纳的 turn id, 是否接纳了任何输入)。
// 未接纳（被别的 pod 持有 / 无额度 / 排水中）不是错误——输入已在队列里，sweeper 会再来。
func (e *Engine) dispatch(ctx context.Context, info pgstore.ThreadInfo) (string, bool, error) {
	tid := protocol.NewThreadID(info.ID)
	if lt := e.liveFor(tid); lt != nil {
		return e.feed(ctx, lt)
	}
	if e.Draining() {
		return "", false, nil
	}
	claimed, err := e.store.ClaimTurn(ctx, tid, e.cfg.PodName)
	if err != nil || !claimed {
		return "", false, err
	}
	tenantID, _ := tenantOf(ctx)
	if e.limiterT != nil && !e.limiterT.TryAcquire(tenantID) {
		_ = e.store.ReleaseTurn(ctx, tid, pgstore.ThreadStatusIdle)
		return "", false, nil
	}
	var agent *pgstore.AgentProfile
	if info.AgentID != nil {
		if a, err := e.store.GetAgent(ctx, *info.AgentID); err == nil {
			agent = &a
		}
	}
	lt, err := e.startLive(ctx, info, agent, e.limiterT != nil)
	if err != nil {
		if e.limiterT != nil {
			e.limiterT.Release(tenantID)
		}
		_ = e.store.ReleaseTurn(ctx, tid, pgstore.ThreadStatusIdle)
		return "", false, fmt.Errorf("start thread session %s: %w", info.ID, err)
	}
	return e.feed(ctx, lt)
}

// feed 把线程队列里全部待接纳输入按序交给会话（运行中即 steer）。
func (e *Engine) feed(ctx context.Context, lt *liveThread) (string, bool, error) {
	pending, err := e.store.PendingInputs(lt.ctx, lt.id)
	if err != nil {
		return "", false, err
	}
	var (
		lastTurn string
		admitted bool
	)
	for _, q := range pending {
		var p queuedPayload
		if err := json.Unmarshal(q.Payload, &p); err != nil {
			_ = e.store.DeleteInput(lt.ctx, q.ID)
			continue
		}
		switch p.Type {
		case queuedInterrupt:
			_, _ = lt.codex.Submit(protocol.Op{Type: protocol.OpInterrupt})
			_ = e.store.DeleteInput(lt.ctx, q.ID)
		default:
			adm, err := lt.codex.SubmitUserMessage(ctx, protocol.Op{Type: protocol.OpUserInput, Items: p.Items}, nil)
			if err != nil {
				if errors.Is(err, core.ErrInternalAgentDied) {
					// 会话正在关闭（turn 刚结束、pump 在释放）：输入留在队列，sweeper 会再派发。
					return lastTurn, admitted, nil
				}
				obs.LoggerFrom(lt.ctx).Warn("input not admitted", "queue_id", q.ID, "error", err)
				continue
			}
			lt.markAdmission(adm)
			if err := e.store.AdmitInput(lt.ctx, q.ID, adm.TurnID); err != nil {
				obs.LoggerFrom(lt.ctx).Warn("admit input failed", "queue_id", q.ID, "error", err)
			}
			lastTurn, admitted = adm.TurnID, true
		}
	}
	return lastTurn, admitted, nil
}

// dispatchPending 在 turn 结束后再喂一次队列；返回是否又开了新一轮。
func (e *Engine) dispatchPending(lt *liveThread) bool {
	if e.Draining() {
		return false
	}
	_, admitted, err := e.feed(lt.ctx, lt)
	if err != nil {
		obs.LoggerFrom(lt.ctx).Warn("dispatch pending failed", "error", err)
		return false
	}
	return admitted
}

// Interrupt 中断当前 turn：本 pod 持有 → 直接下发；否则入队交给持有方；空闲线程无操作。
func (e *Engine) Interrupt(ctx context.Context, threadID string) error {
	tid := protocol.NewThreadID(threadID)
	info, err := e.store.GetThreadInfo(ctx, tid)
	if err != nil {
		return err
	}
	if lt := e.liveFor(tid); lt != nil {
		_, err := lt.codex.Submit(protocol.Op{Type: protocol.OpInterrupt})
		return err
	}
	if info.Status != pgstore.ThreadStatusRunning {
		return nil
	}
	_, err = e.enqueue(ctx, tid, queuedPayload{Type: queuedInterrupt}, pgstore.QueueKindSteer)
	return err
}

// ResumeThread 恢复被中断（pod 死 / 排水）的线程：状态回 idle 并把队列里未接纳的输入重新派发；
// 不自动重跑已开始的 turn（spec-1.8 §3.8：避免重复动作）——rollout 里的历史在下一轮回放。
func (e *Engine) ResumeThread(ctx context.Context, threadID string) error {
	tid := protocol.NewThreadID(threadID)
	info, err := e.store.GetThreadInfo(ctx, tid)
	if err != nil {
		return err
	}
	if info.Status == pgstore.ThreadStatusInterrupted {
		if err := e.store.MarkIdle(ctx, tid); err != nil {
			return err
		}
		info.Status = pgstore.ThreadStatusIdle
	}
	_, _, err = e.dispatch(ctx, info)
	return err
}

// Dispatch 供 sweeper 调用：按 (tenant, thread) 派发队列里的输入。
func (e *Engine) Dispatch(ctx context.Context, threadID string) error {
	info, err := e.store.GetThreadInfo(ctx, protocol.NewThreadID(threadID))
	if err != nil {
		return err
	}
	_, _, err = e.dispatch(ctx, info)
	return err
}
