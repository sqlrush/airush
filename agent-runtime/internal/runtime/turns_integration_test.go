//go:build integration

package runtime

import (
	"context"
	"encoding/json"
	"strings"
	"testing"
	"time"

	"github.com/sqlrush/codexgo/pkg/protocol"

	"github.com/sqlrush/airush/agent-runtime/internal/pgstore"
	"github.com/sqlrush/airush/agent-runtime/internal/scheduler"
	"github.com/sqlrush/airush/libs/apierror"
)

// TestTurnRoundTrip spec-1.8 T4/T5/T8 的运行时面：建线程 → 发一轮 → 事件（session_meta、
// turn_context、用户/助手 response_item、task_started/complete）落 PG 且 seq 单调；线程回 idle、
// 会话释放；LLM 请求带租户头 + 记账一次；agent 的 instruction_doc 进 developer 指令、
// default_model 进模型名。
func TestTurnRoundTrip(t *testing.T) {
	ctx, tenantID := newTenant(t)
	llmSrv := newFakeLLM(t)
	e, stubs := newEngine(t, llmSrv, "pod-a")
	agentID := newAgent(t, tenantID, "chat-fast", "You are the test agent.")

	ref, err := e.StartThread(ctx, StartThreadInput{AgentID: agentID, Title: "第一轮"})
	if err != nil {
		t.Fatalf("start thread: %v", err)
	}
	info, err := testStore.GetThreadInfo(ctx, protocol.NewThreadID(ref.ThreadID))
	if err != nil || info.Model != "chat-fast" || info.Title != "第一轮" || info.AgentID == nil {
		t.Fatalf("thread row = %+v (%v)", info, err)
	}

	turn, err := e.SubmitTurn(ctx, ref.ThreadID, textInput("你好"))
	if err != nil {
		t.Fatalf("submit turn: %v", err)
	}
	if turn.Queued || turn.TurnID == "" {
		t.Fatalf("turn = %+v, want admitted new turn", turn)
	}
	waitStatus(t, ctx, ref.ThreadID, pgstore.ThreadStatusIdle, 10*time.Second)
	if e.LiveCount() != 0 {
		t.Fatalf("live sessions after turn = %d, want 0 (stateless)", e.LiveCount())
	}

	types := eventTypes(t, ctx, ref.ThreadID)
	for _, want := range []string{pgstore.EventTypeSessionMeta, pgstore.EventTypeTurnContext, pgstore.EventTypeResponseItem, "task_started", "task_complete", "agent_message"} {
		if !contains(types, want) {
			t.Fatalf("events missing %s: %v", want, types)
		}
	}
	if types[0] != pgstore.EventTypeSessionMeta {
		t.Fatalf("first event = %s, want session_meta", types[0])
	}
	evs, _ := testStore.ReadEvents(ctx, protocol.NewThreadID(ref.ThreadID), 0, 0)
	for i, ev := range evs {
		if ev.Seq != int64(i+1) {
			t.Fatalf("seq gap at %d: %d", i, ev.Seq)
		}
		if ev.EventType == "task_started" && (ev.TurnID == nil || *ev.TurnID != turn.TurnID) {
			t.Fatalf("task_started turn_id column = %v, want %s", ev.TurnID, turn.TurnID)
		}
	}

	if llmSrv.Requests.Load() != 1 || stubs.count() != 1 {
		t.Fatalf("llm requests = %d, meter records = %d", llmSrv.Requests.Load(), stubs.count())
	}
	if stubs.tenants[0] != tenantID {
		t.Fatalf("meter tenant = %s, want %s", stubs.tenants[0], tenantID)
	}
	req := llmSrv.requests()[0]
	if req["model"] != "chat-fast" {
		t.Fatalf("model sent = %v, want chat-fast", req["model"])
	}
	raw, _ := json.Marshal(req)
	if !strings.Contains(string(raw), "You are the test agent.") || !strings.Contains(string(raw), "AIRush") {
		t.Fatalf("request lacks agent/base instructions: %s", raw[:min(len(raw), 400)])
	}

	// 第二轮：resume（LoadHistory 回放），历史里带上一轮的助手回复
	turn2, err := e.SubmitTurn(ctx, ref.ThreadID, textInput("再来"))
	if err != nil || turn2.Queued {
		t.Fatalf("second turn = %+v (%v)", turn2, err)
	}
	waitStatus(t, ctx, ref.ThreadID, pgstore.ThreadStatusIdle, 10*time.Second)
	raw2, _ := json.Marshal(llmSrv.requests()[1])
	if !strings.Contains(string(raw2), "mock reply 1") || !strings.Contains(string(raw2), "再来") {
		t.Fatalf("second request lacks replayed history: %s", raw2[:min(len(raw2), 600)])
	}
	if n := countType(eventTypes(t, ctx, ref.ThreadID), pgstore.EventTypeSessionMeta); n != 1 {
		t.Fatalf("session_meta recorded %d times, want 1 (resume must not re-write)", n)
	}
}

// TestSteerIntoRunningTurn spec-1.8 T6：运行中的线程收到新输入 → 入队（steer）→ 当前 turn 接纳
// （Queued=true、同一 turn id）→ 输入进入同一轮的下一次采样。
func TestSteerIntoRunningTurn(t *testing.T) {
	ctx, _ := newTenant(t)
	llmSrv := newFakeLLM(t)
	llmSrv.Hold = make(chan struct{})
	e, _ := newEngine(t, llmSrv, "pod-a")
	ref, _ := e.StartThread(ctx, StartThreadInput{})

	first, err := e.SubmitTurn(ctx, ref.ThreadID, textInput("第一条"))
	if err != nil || first.Queued {
		t.Fatalf("first = %+v (%v)", first, err)
	}
	// 等采样请求真的到了假供应商（turn 在跑）
	waitFor(t, 5*time.Second, func() bool { return llmSrv.Requests.Load() >= 1 })

	steer, err := e.SubmitTurn(ctx, ref.ThreadID, textInput("补充一句"))
	if err != nil {
		t.Fatalf("steer: %v", err)
	}
	if !steer.Queued || steer.TurnID != first.TurnID {
		t.Fatalf("steer = %+v, want queued into turn %s", steer, first.TurnID)
	}
	close(llmSrv.Hold)
	waitStatus(t, ctx, ref.ThreadID, pgstore.ThreadStatusIdle, 15*time.Second)
	// 第二次采样带上了 steer 的输入
	reqs := llmSrv.requests()
	if len(reqs) < 2 {
		t.Fatalf("requests = %d, want ≥2 (steer triggers another sampling)", len(reqs))
	}
	raw, _ := json.Marshal(reqs[len(reqs)-1])
	if !strings.Contains(string(raw), "补充一句") {
		t.Fatalf("steered input not sampled: %s", raw[:min(len(raw), 600)])
	}
	pending, _ := testStore.PendingInputs(ctx, protocol.NewThreadID(ref.ThreadID))
	if len(pending) != 0 {
		t.Fatalf("queue not admitted: %+v", pending)
	}
}

// TestInterrupt：中断运行中的 turn → turn_aborted 事件、线程回 idle、可再发下一轮。
func TestInterrupt(t *testing.T) {
	ctx, _ := newTenant(t)
	llmSrv := newFakeLLM(t)
	llmSrv.Hold = make(chan struct{})
	e, _ := newEngine(t, llmSrv, "pod-a")
	ref, _ := e.StartThread(ctx, StartThreadInput{})
	if _, err := e.SubmitTurn(ctx, ref.ThreadID, textInput("慢")); err != nil {
		t.Fatalf("submit: %v", err)
	}
	waitFor(t, 5*time.Second, func() bool { return llmSrv.Requests.Load() >= 1 })
	if err := e.Interrupt(ctx, ref.ThreadID); err != nil {
		t.Fatalf("interrupt: %v", err)
	}
	waitStatus(t, ctx, ref.ThreadID, pgstore.ThreadStatusIdle, 10*time.Second)
	if !contains(eventTypes(t, ctx, ref.ThreadID), "turn_aborted") {
		t.Fatalf("no turn_aborted: %v", eventTypes(t, ctx, ref.ThreadID))
	}
	close(llmSrv.Hold)
	llmSrv.Hold = nil
	if turn, err := e.SubmitTurn(ctx, ref.ThreadID, textInput("再来")); err != nil || turn.Queued {
		t.Fatalf("turn after interrupt = %+v (%v)", turn, err)
	}
	waitStatus(t, ctx, ref.ThreadID, pgstore.ThreadStatusIdle, 10*time.Second)
}

// TestTenantConcurrencyLimit spec-1.8 T7：租户并发上限 1 → 第二条线程的 turn 入队不启动；
// 第一条跑完后 sweeper 派发第二条；另一租户不受影响。
func TestTenantConcurrencyLimit(t *testing.T) {
	ctxA, _ := newTenant(t)
	ctxB, _ := newTenant(t)
	llmSrv := newFakeLLM(t)
	llmSrv.Hold = make(chan struct{})
	e, _ := newEngine(t, llmSrv, "pod-a")
	e.SetLimiter(scheduler.NewTenantLimiter(1))

	a1, _ := e.StartThread(ctxA, StartThreadInput{})
	a2, _ := e.StartThread(ctxA, StartThreadInput{})
	b1, _ := e.StartThread(ctxB, StartThreadInput{})
	if turn, err := e.SubmitTurn(ctxA, a1.ThreadID, textInput("a1")); err != nil || turn.Queued {
		t.Fatalf("a1 = %+v (%v)", turn, err)
	}
	waitFor(t, 5*time.Second, func() bool { return llmSrv.Requests.Load() >= 1 })
	turn, err := e.SubmitTurn(ctxA, a2.ThreadID, textInput("a2"))
	if err != nil || !turn.Queued || turn.TurnID != "" {
		t.Fatalf("a2 = %+v (%v), want queued without turn", turn, err)
	}
	if info, _ := testStore.GetThreadInfo(ctxA, protocol.NewThreadID(a2.ThreadID)); info.Status != pgstore.ThreadStatusIdle {
		t.Fatalf("a2 must stay idle while queued: %+v", info)
	}
	if turn, err := e.SubmitTurn(ctxB, b1.ThreadID, textInput("b1")); err != nil || turn.Queued {
		t.Fatalf("other tenant blocked: %+v (%v)", turn, err)
	}
	close(llmSrv.Hold)
	waitStatus(t, ctxA, a1.ThreadID, pgstore.ThreadStatusIdle, 15*time.Second)
	// sweeper 兜底派发 a2
	sw := scheduler.NewSweeper(testStore, e, time.Second, nil)
	waitFor(t, 15*time.Second, func() bool {
		sw.Sweep(context.Background())
		pending, _ := testStore.PendingInputs(ctxA, protocol.NewThreadID(a2.ThreadID))
		return len(pending) == 0
	})
	waitStatus(t, ctxA, a2.ThreadID, pgstore.ThreadStatusIdle, 15*time.Second)
	if !contains(eventTypes(t, ctxA, a2.ThreadID), "task_complete") {
		t.Fatalf("a2 never ran: %v", eventTypes(t, ctxA, a2.ThreadID))
	}
}

// TestCrossPodResumeAndRecovery spec-1.8 T12/T17：pod-a 跑完第一轮后 pod-b 接第二轮（按 rollout
// 回放）；pod-a "死"在第三轮里（心跳过期）→ 新实例恢复扫描把线程标 interrupted → ResumeThread
// 回 idle 并重投队列里未接纳的输入。
func TestCrossPodResumeAndRecovery(t *testing.T) {
	ctx, _ := newTenant(t)
	llmSrv := newFakeLLM(t)
	podA, _ := newEngine(t, llmSrv, "pod-a")
	podB, _ := newEngine(t, llmSrv, "pod-b")
	ref, _ := podA.StartThread(ctx, StartThreadInput{})
	if _, err := podA.SubmitTurn(ctx, ref.ThreadID, textInput("一")); err != nil {
		t.Fatalf("turn 1: %v", err)
	}
	waitStatus(t, ctx, ref.ThreadID, pgstore.ThreadStatusIdle, 10*time.Second)
	if _, err := podB.SubmitTurn(ctx, ref.ThreadID, textInput("二")); err != nil {
		t.Fatalf("turn 2 on pod-b: %v", err)
	}
	waitStatus(t, ctx, ref.ThreadID, pgstore.ThreadStatusIdle, 10*time.Second)
	raw, _ := json.Marshal(llmSrv.requests()[1])
	if !strings.Contains(string(raw), "mock reply 1") {
		t.Fatalf("pod-b did not replay pod-a's history: %s", raw[:min(len(raw), 600)])
	}

	// pod-a 领取第三轮后"死掉"：直接把行改成心跳过期的 running（不经 pod-a 的会话）
	tid := protocol.NewThreadID(ref.ThreadID)
	if ok, err := testStore.ClaimTurn(ctx, tid, "pod-a"); err != nil || !ok {
		t.Fatalf("claim: %v %v", ok, err)
	}
	if _, err := testPool.Exec(context.Background(), `UPDATE agent_threads SET heartbeat_at = now() - interval '1 hour' WHERE id = $1`, ref.ThreadID); err != nil {
		t.Fatalf("backdate: %v", err)
	}
	// 输入在 pod-a 死之前入了队（没接纳）
	if turn, err := podB.SubmitTurn(ctx, ref.ThreadID, textInput("三")); err != nil || !turn.Queued {
		t.Fatalf("input while held elsewhere = %+v (%v), want queued", turn, err)
	}
	podC, _ := newEngine(t, llmSrv, "pod-c")
	n, err := podC.Recover(context.Background())
	if err != nil || n < 1 {
		t.Fatalf("recover = %d (%v)", n, err)
	}
	if info, _ := testStore.GetThreadInfo(ctx, tid); info.Status != pgstore.ThreadStatusInterrupted {
		t.Fatalf("status after recover = %s", info.Status)
	}
	if err := podC.ResumeThread(ctx, ref.ThreadID); err != nil {
		t.Fatalf("resume: %v", err)
	}
	waitStatus(t, ctx, ref.ThreadID, pgstore.ThreadStatusIdle, 15*time.Second)
	raw3, _ := json.Marshal(llmSrv.requests()[len(llmSrv.requests())-1])
	if !strings.Contains(string(raw3), "三") {
		t.Fatalf("queued input not re-dispatched after resume: %s", raw3[:min(len(raw3), 600)])
	}
}

// TestDrain spec-1.8 T16：排水期间在飞 turn 跑完 → idle；超时的 turn 中断 → interrupted +
// turn_aborted；排水中不再领取新 turn（入队等别的 pod）。
func TestDrain(t *testing.T) {
	ctx, _ := newTenant(t)
	llmSrv := newFakeLLM(t)
	llmSrv.Hold = make(chan struct{})
	e, _ := newEngine(t, llmSrv, "pod-a")
	slow, _ := e.StartThread(ctx, StartThreadInput{})
	if _, err := e.SubmitTurn(ctx, slow.ThreadID, textInput("慢")); err != nil {
		t.Fatalf("submit: %v", err)
	}
	waitFor(t, 5*time.Second, func() bool { return llmSrv.Requests.Load() >= 1 })

	done := make(chan struct{})
	go func() {
		e.Drain(context.Background(), 2*time.Second)
		close(done)
	}()
	waitFor(t, 2*time.Second, e.Draining)
	// 排水中新 turn 不领取
	other, _ := e.StartThread(ctx, StartThreadInput{})
	if turn, err := e.SubmitTurn(ctx, other.ThreadID, textInput("新")); err != nil || !turn.Queued {
		t.Fatalf("turn during drain = %+v (%v), want queued", turn, err)
	}
	select {
	case <-done:
	case <-time.After(15 * time.Second):
		t.Fatal("drain did not finish")
	}
	info := waitStatus(t, ctx, slow.ThreadID, pgstore.ThreadStatusInterrupted, 5*time.Second)
	if info.RunningPod != nil {
		t.Fatalf("interrupted thread still has running_pod: %+v", info)
	}
	if !contains(eventTypes(t, ctx, slow.ThreadID), "turn_aborted") {
		t.Fatalf("no turn_aborted after drain: %v", eventTypes(t, ctx, slow.ThreadID))
	}
	close(llmSrv.Hold)
	// 排水完成的快 turn 路径
	llmSrv2 := newFakeLLM(t)
	e2, _ := newEngine(t, llmSrv2, "pod-b")
	fast, _ := e2.StartThread(ctx, StartThreadInput{})
	if _, err := e2.SubmitTurn(ctx, fast.ThreadID, textInput("快")); err != nil {
		t.Fatalf("submit fast: %v", err)
	}
	e2.Drain(context.Background(), 10*time.Second)
	waitStatus(t, ctx, fast.ThreadID, pgstore.ThreadStatusIdle, 5*time.Second)
}

// TestFailClosedWithoutTenant：无租户 ctx 的任何入口都拒绝（AR_TENANT_CONTEXT_MISSING）。
func TestFailClosedWithoutTenant(t *testing.T) {
	llmSrv := newFakeLLM(t)
	e, _ := newEngine(t, llmSrv, "pod-a")
	if _, err := e.StartThread(context.Background(), StartThreadInput{}); !isCode(err, apierror.CodeTenantContextMissing) {
		t.Fatalf("start without tenant: %v", err)
	}
	if _, err := e.SubmitTurn(context.Background(), protocol.NewThreadIDV7().String(), textInput("x")); !isCode(err, apierror.CodeTenantContextMissing) {
		t.Fatalf("submit without tenant: %v", err)
	}
	if _, err := e.Events(context.Background(), protocol.NewThreadIDV7().String(), 0); !isCode(err, apierror.CodeTenantContextMissing) {
		t.Fatalf("events without tenant: %v", err)
	}
}

func isCode(err error, code apierror.Code) bool {
	ae, ok := apierror.FromError(err)
	return ok && ae.Code == code
}

func countType(types []string, want string) int {
	n := 0
	for _, s := range types {
		if s == want {
			n++
		}
	}
	return n
}

func waitFor(t *testing.T, d time.Duration, cond func() bool) {
	t.Helper()
	deadline := time.Now().Add(d)
	for time.Now().Before(deadline) {
		if cond() {
			return
		}
		time.Sleep(20 * time.Millisecond)
	}
	t.Fatalf("condition not met within %s", d)
}
