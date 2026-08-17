package runtime

import (
	"context"
	"strings"
	"sync"
	"time"

	"github.com/sqlrush/codexgo/pkg/core"
	"github.com/sqlrush/codexgo/pkg/protocol"
	"github.com/sqlrush/codexgo/pkg/rollout"

	"github.com/sqlrush/airush/agent-runtime/internal/pgstore"
	"github.com/sqlrush/airush/agent-runtime/internal/tenantctx"
	"github.com/sqlrush/airush/libs/obs"
)

// liveThread 是本 pod 上持有（已 ClaimTurn）的一条线程的瞬时状态：codexgo 会话 + 事件泵 +
// 心跳。它只在"领取 → 跑完全部待接纳输入 → 释放"这一段存在；释放即关闭会话，下一轮
// 由任何 pod 按 rollout 恢复（AD-1）。
type liveThread struct {
	id       protocol.ThreadID
	tenantID string
	ctx      context.Context
	cancel   context.CancelFunc
	thread   *core.CodexThread
	codex    *core.Codex

	mu      sync.Mutex
	turn    string
	running bool
	// steered 记最近一次接纳是 steer（进了运行中的 turn）还是新开 turn。
	steered bool
	// holdsSlot 记本线程占着一个租户并发额度（释放时归还）。
	holdsSlot bool
	// idle 在 turn 结束时关闭并重建，等待者据此感知"这一轮跑完了"。
	idleCh chan struct{}
	// released 在 liveThread 释放后关闭。
	released chan struct{}
	relOnce  sync.Once
}

func (l *liveThread) turnID() string {
	l.mu.Lock()
	defer l.mu.Unlock()
	return l.turn
}

func (l *liveThread) turnRunning() bool {
	l.mu.Lock()
	defer l.mu.Unlock()
	return l.running
}

func (l *liveThread) setTurn(id string) {
	l.mu.Lock()
	defer l.mu.Unlock()
	l.turn = id
	l.running = true
}

// markAdmission 记录 core 的接纳结果（Started = 新 turn；Steered = 并入运行中的 turn）。
func (l *liveThread) markAdmission(adm core.UserMessageAdmission) {
	l.mu.Lock()
	defer l.mu.Unlock()
	l.turn = adm.TurnID
	l.running = true
	l.steered = adm.Kind == core.UserMessageAdmissionSteered
}

// endTurn 标记 turn 结束并唤醒等待者。
func (l *liveThread) endTurn() {
	l.mu.Lock()
	defer l.mu.Unlock()
	l.running = false
	close(l.idleCh)
	l.idleCh = make(chan struct{})
}

func (l *liveThread) idleWaiter() <-chan struct{} {
	l.mu.Lock()
	defer l.mu.Unlock()
	return l.idleCh
}

// startLive 领取后启动线程会话：last_seq==0 → 全新 spawn（写 session_meta），否则按 store 历史
// resume（0.147 ReadThread+IncludeHistory → 回放）。会话 ctx 由 tenantctx.Session 派生（不随请求取消）。
func (e *Engine) startLive(reqCtx context.Context, info pgstore.ThreadInfo, agent *pgstore.AgentProfile, holdsSlot bool) (*liveThread, error) {
	tenantID, err := tenantOf(reqCtx)
	if err != nil {
		return nil, err
	}
	agentID := ""
	if info.AgentID != nil {
		agentID = *info.AgentID
	}
	sessCtx := tenantctx.Session(reqCtx, tenantctx.Info{TenantID: tenantID, AgentID: agentID, ThreadID: info.ID, TraceID: info.ID}, e.logger)
	sessCtx, cancel := context.WithCancel(sessCtx)
	threadID := protocol.NewThreadID(info.ID)
	cfg := e.sessionConfig(info, agent)

	var nt core.NewThread
	if info.LastSeq == 0 {
		nt, err = e.tm.StartThreadWithOptions(sessCtx, core.StartThreadOptions{
			Configuration:  cfg,
			InitialHistory: rollout.InitialHistory{Kind: rollout.InitialHistoryKindNew},
			ThreadID:       &threadID,
		})
	} else {
		nt, err = e.tm.ResumeThreadByID(sessCtx, cfg, threadID, airushSessionSource())
	}
	if err != nil {
		cancel()
		return nil, err
	}
	lt := &liveThread{
		id: threadID, tenantID: tenantID, ctx: sessCtx, cancel: cancel,
		thread: nt.Thread, codex: nt.Thread.Codex(),
		idleCh: make(chan struct{}), released: make(chan struct{}), holdsSlot: holdsSlot,
	}
	e.mu.Lock()
	e.live[info.ID] = lt
	e.mu.Unlock()
	go e.pump(lt)
	go e.heartbeat(lt)
	return lt, nil
}

// sessionConfig 是线程的 codexgo 会话配置：无本地执行、无审批交互（AskForApproval never——
// 没有本地工具需要问）、平台基础指令 + agent 指令作 developer instructions。
func (e *Engine) sessionConfig(info pgstore.ThreadInfo, agent *pgstore.AgentProfile) core.SessionConfiguration {
	model := info.Model
	if model == "" {
		model = e.cfg.DefaultModel
	}
	cfg := core.SessionConfiguration{
		ProviderID:       providerName,
		BaseInstructions: baseInstructions,
		ApprovalPolicy:   protocol.AskForApproval{Kind: protocol.AskForApprovalNever},
		SandboxMode:      protocol.SandboxModeReadOnly,
		Cwd:              "/",
		CollaborationMode: protocol.CollaborationMode{
			Mode:     protocol.ModeKindDefault,
			Settings: protocol.Settings{Model: model},
		},
	}
	if agent != nil && strings.TrimSpace(agent.InstructionDoc) != "" {
		// agent 指令并入系统提示（port 的会话初始上下文不注入 DeveloperInstructions；
		// 只在压缩重建时用——base instructions 是每次采样都带的稳定位置）。
		cfg.BaseInstructions = baseInstructions + "\n\n# Agent instructions\n\n" + strings.TrimSpace(agent.InstructionDoc)
	}
	return cfg
}

// baseInstructions 是平台基础系统提示（Stage 1 定值；上下文装配的细化在 memory-knowledge §10）。
const baseInstructions = `You are AIRush, a database operations assistant running inside a multi-tenant platform.
You help operators understand and diagnose their database instances using the tools provided.
You have no local shell or file system; every capability is a tool. Read-only tools run directly;
tools that change customer systems require human approval, which is not available in this stage —
when such an action is needed, explain exactly what should be done and why instead of attempting it.
Answer in the user's language, be precise, and cite the data you used.`

// pump 消费会话事件：跟踪 turn 生命周期（task_started / task_complete / turn_aborted），
// turn 结束后先接纳该线程在 PG 队列里的待处理输入，没有了才释放线程。事件本身由 core 经
// RolloutRecorder 落库，这里不再写。
func (e *Engine) pump(lt *liveThread) {
	logger := obs.LoggerFrom(lt.ctx)
	for {
		ev, err := lt.codex.NextEvent(lt.ctx)
		if err != nil {
			e.release(lt, pgstore.ThreadStatusIdle)
			return
		}
		switch ev.Msg.Type {
		case protocol.EventMsgKindTurnStarted:
			if ev.Msg.TurnStarted != nil {
				lt.setTurn(ev.Msg.TurnStarted.TurnID)
			}
		case protocol.EventMsgKindTurnComplete, protocol.EventMsgKindTurnAborted:
			lt.endTurn()
			e.notifier.Notify(lt.id.String())
			if ev.Msg.Type == protocol.EventMsgKindTurnAborted && e.Draining() {
				// 排水中止的 turn：线程标 interrupted（可 resume），队列里的输入留给下一任持有者。
				e.release(lt, pgstore.ThreadStatusInterrupted)
				return
			}
			if e.dispatchPending(lt) {
				continue
			}
			e.release(lt, pgstore.ThreadStatusIdle)
			return
		case protocol.EventMsgKindError:
			if ev.Msg.Error != nil {
				logger.Warn("agent turn error", "message", ev.Msg.Error.Message)
			}
		}
	}
}

// heartbeat 持有期间定期刷新 agent_threads.heartbeat_at。
func (e *Engine) heartbeat(lt *liveThread) {
	t := time.NewTicker(e.cfg.HeartbeatInterval)
	defer t.Stop()
	for {
		select {
		case <-lt.released:
			return
		case <-lt.ctx.Done():
			return
		case <-t.C:
			if err := e.store.Heartbeat(lt.ctx, lt.id, e.cfg.PodName); err != nil {
				obs.LoggerFrom(lt.ctx).Warn("heartbeat failed", "error", err)
			}
		}
	}
}

// release 结束持有：状态回 idle/interrupted，关闭会话，移出在飞表。幂等。
func (e *Engine) release(lt *liveThread, to pgstore.ThreadStatus) {
	lt.relOnce.Do(func() {
		e.mu.Lock()
		if e.live[lt.id.String()] == lt {
			delete(e.live, lt.id.String())
		}
		e.mu.Unlock()
		if err := e.store.ReleaseTurn(lt.ctx, lt.id, to); err != nil {
			obs.LoggerFrom(lt.ctx).Warn("release turn failed", "error", err)
		}
		if lt.holdsSlot && e.limiterT != nil {
			e.limiterT.Release(lt.tenantID)
		}
		shutdownCtx, cancel := context.WithTimeout(context.WithoutCancel(lt.ctx), 5*time.Second)
		defer cancel()
		_ = lt.codex.Shutdown(shutdownCtx)
		lt.cancel()
		close(lt.released)
		e.notifier.Notify(lt.id.String())
	})
}
