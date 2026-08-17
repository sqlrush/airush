//go:build integration

package pgstore

import (
	"context"
	"encoding/json"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/sqlrush/codexgo/pkg/protocol"
	"github.com/sqlrush/codexgo/pkg/rollout"
	"github.com/sqlrush/codexgo/pkg/threadstore"
	"github.com/sqlrush/codexgo/pkg/threadstore/contracttest"

	"github.com/sqlrush/airush/libs/apierror"
)

// TestTurnStateMachine 线程状态机：idle→running（Claim）→idle/interrupted（Release）；
// 运行中再 Claim 不成功（会话内串行）；心跳只刷新本 pod 持有的 running 线程；
// 未知线程 Claim → AR_AGENT_THREAD_NOT_FOUND；SetThreadAttributes 只改给定字段。
func TestTurnStateMachine(t *testing.T) {
	ctx := tenantCtx(t)
	id := mustCreate(t, ctx, 10)

	claimed, err := testStore.ClaimTurn(ctx, id, "pod-a")
	if err != nil || !claimed {
		t.Fatalf("first claim = (%v, %v)", claimed, err)
	}
	claimed, err = testStore.ClaimTurn(ctx, id, "pod-b")
	if err != nil || claimed {
		t.Fatalf("second claim while running = (%v, %v)", claimed, err)
	}
	info, err := testStore.GetThreadInfo(ctx, id)
	if err != nil {
		t.Fatalf("info: %v", err)
	}
	if info.Status != ThreadStatusRunning || info.RunningPod == nil || *info.RunningPod != "pod-a" || info.HeartbeatAt == nil {
		t.Fatalf("running info = %+v", info)
	}
	before := *info.HeartbeatAt
	waitFor(t, 5*time.Second, func() bool {
		if err := testStore.Heartbeat(ctx, id, "pod-a"); err != nil {
			t.Fatalf("heartbeat: %v", err)
		}
		cur, err := testStore.GetThreadInfo(ctx, id)
		return err == nil && cur.HeartbeatAt.After(before)
	})
	if err := testStore.Heartbeat(ctx, id, "pod-b"); err != nil { // 别的 pod 心跳无效但不报错
		t.Fatalf("foreign heartbeat: %v", err)
	}

	if err := testStore.ReleaseTurn(ctx, id, ThreadStatusArchived); !isCode(err, apierror.CodeValidationFailed) {
		t.Fatalf("release to archived must be rejected: %v", err)
	}
	if err := testStore.ReleaseTurn(ctx, id, ThreadStatusInterrupted); err != nil {
		t.Fatalf("release: %v", err)
	}
	info, _ = testStore.GetThreadInfo(ctx, id)
	if info.Status != ThreadStatusInterrupted || info.RunningPod != nil || info.HeartbeatAt != nil {
		t.Fatalf("interrupted info = %+v", info)
	}
	// interrupted 可再次 Claim（resume）
	if claimed, err := testStore.ClaimTurn(ctx, id, "pod-c"); err != nil || !claimed {
		t.Fatalf("resume claim = (%v, %v)", claimed, err)
	}
	if err := testStore.ReleaseTurn(ctx, id, ThreadStatusIdle); err != nil {
		t.Fatalf("release idle: %v", err)
	}

	if _, err := testStore.ClaimTurn(ctx, contracttest.ThreadID(999), "pod-a"); !isCode(err, apierror.CodeAgentThreadNotFound) {
		t.Fatalf("claim unknown: %v", err)
	}

	agentID := uuid.NewString()
	err = testStore.SetThreadAttributes(ctx, id, ThreadAttributes{Model: "chat-fast", Title: "首轮标题"})
	if err != nil {
		t.Fatalf("set attrs: %v", err)
	}
	// agent_id 复合外键 → 不存在的 agent 被库拒绝（AD-10 第二道锁）
	if err := testStore.SetThreadAttributes(ctx, id, ThreadAttributes{AgentID: &agentID}); err == nil {
		t.Fatalf("dangling agent_id must be rejected")
	}
	info, _ = testStore.GetThreadInfo(ctx, id)
	if info.Model != "chat-fast" || info.Title != "首轮标题" || info.AgentID != nil {
		t.Fatalf("attrs = %+v", info)
	}
	st, err := testStore.Threads().ReadThread(ctx, threadstore.ReadThreadParams{ThreadID: id})
	if err != nil || st.Name == nil || *st.Name != "首轮标题" || st.Model == nil || *st.Model != "chat-fast" {
		t.Fatalf("stored thread projection = %+v (%v)", st, err)
	}
}

// TestRecoveryScan spec-1.8 §3.8 / T17 的存储面：心跳过期的 running 线程被标 interrupted，
// 心跳新鲜的不动；扫描跨全部租户但只走 RLS 事务路径。
func TestRecoveryScan(t *testing.T) {
	ctxA := tenantCtx(t)
	ctxB := otherTenantCtx(t)
	stale := mustCreate(t, ctxA, 20)
	fresh := mustCreate(t, ctxA, 21)
	staleB := mustCreate(t, ctxB, 22)
	for _, c := range []struct {
		ctx context.Context
		id  protocol.ThreadID
	}{{ctxA, stale}, {ctxA, fresh}, {ctxB, staleB}} {
		if ok, err := testStore.ClaimTurn(c.ctx, c.id, "dead-pod"); err != nil || !ok {
			t.Fatalf("claim %s: (%v, %v)", c.id, ok, err)
		}
	}
	// 用超级用户直接把两条心跳拨到过去（测试夹具，不是产品路径）
	for _, id := range []protocol.ThreadID{stale, staleB} {
		if _, err := testPool.Exec(context.Background(), `UPDATE agent_threads SET heartbeat_at = now() - interval '10 minutes' WHERE id = $1`, id.String()); err != nil {
			t.Fatalf("backdate heartbeat: %v", err)
		}
	}
	hits, err := testStore.MarkStaleRunningInterrupted(context.Background(), 2*time.Minute)
	if err != nil {
		t.Fatalf("scan: %v", err)
	}
	got := map[string]bool{}
	for _, h := range hits {
		got[h.ThreadID.String()] = true
		if h.RunningPod == nil || *h.RunningPod != "dead-pod" {
			t.Fatalf("hit without pod: %+v", h)
		}
	}
	if !got[stale.String()] || !got[staleB.String()] || got[fresh.String()] {
		t.Fatalf("hits = %v", got)
	}
	if info, _ := testStore.GetThreadInfo(ctxA, fresh); info.Status != ThreadStatusRunning {
		t.Fatalf("fresh thread must stay running: %+v", info)
	}
	if info, _ := testStore.GetThreadInfo(ctxB, staleB); info.Status != ThreadStatusInterrupted {
		t.Fatalf("stale thread in tenant B must be interrupted: %+v", info)
	}
	// 再次扫描无命中（幂等）
	if again, err := testStore.MarkStaleRunningInterrupted(context.Background(), 2*time.Minute); err != nil || len(again) != 0 {
		t.Fatalf("second scan = (%v, %v)", again, err)
	}
	if err := testStore.EnsureEventPartitions(context.Background()); err != nil {
		t.Fatalf("ensure partitions: %v", err)
	}
}

// TestInputQueue steer/排队输入外置队列：入队按序、接纳后不再列出、租户隔离由 RLS 兜底、
// 未知线程入队 → ThreadNotFound。
func TestInputQueue(t *testing.T) {
	ctx := tenantCtx(t)
	id := mustCreate(t, ctx, 30)
	payload := json.RawMessage(`{"type":"user_input","items":[{"type":"text","text":"再看一下"}]}`)
	q1, q2 := uuid.NewString(), uuid.NewString()
	if err := testStore.EnqueueInput(ctx, id, q1, QueueKindSteer, payload); err != nil {
		t.Fatalf("enqueue: %v", err)
	}
	if err := testStore.EnqueueInput(ctx, id, q2, QueueKindQueued, payload); err != nil {
		t.Fatalf("enqueue: %v", err)
	}
	pending, err := testStore.PendingInputs(ctx, id)
	if err != nil || len(pending) != 2 || pending[0].ID != q1 || pending[0].Kind != QueueKindSteer || pending[1].Kind != QueueKindQueued {
		t.Fatalf("pending = %+v (%v)", pending, err)
	}
	if string(pending[0].Payload) == "" || pending[0].ThreadID.String() != id.String() {
		t.Fatalf("pending row = %+v", pending[0])
	}
	if err := testStore.AdmitInput(ctx, q1, uuid.NewString()); err != nil {
		t.Fatalf("admit: %v", err)
	}
	pending, _ = testStore.PendingInputs(ctx, id)
	if len(pending) != 1 || pending[0].ID != q2 {
		t.Fatalf("pending after admit = %+v", pending)
	}
	if other, _ := testStore.PendingInputs(otherTenantCtx(t), id); len(other) != 0 {
		t.Fatalf("queue visible across tenants: %+v", other)
	}
	if err := testStore.EnqueueInput(ctx, contracttest.ThreadID(998), q1, QueueKindSteer, payload); !isThreadNotFound(err) {
		t.Fatalf("enqueue unknown thread: %v", err)
	}
	// 删除线程级联清队列
	if err := testStore.Threads().DeleteThread(ctx, threadstore.DeleteThreadParams{ThreadID: id}); err != nil {
		t.Fatalf("delete: %v", err)
	}
	if _, err := testStore.PendingInputs(ctx, id); !isThreadNotFound(err) && err != nil {
		t.Fatalf("pending after delete: %v", err)
	}
}

// TestLatestModelContextAndPagination LoadLatestModelContext 只回最近一次压缩起的后缀（无压缩全量）；
// ListItems / ListTurns 分页游标可续。
func TestLatestModelContextAndPagination(t *testing.T) {
	ctx := tenantCtx(t)
	id := mustCreate(t, ctx, 40)
	turn1, turn2 := uuid.NewString(), uuid.NewString()
	mustAppend(t, ctx, id,
		rollout.NewEventMsgItem(protocol.EventMsg{Type: protocol.EventMsgKindTurnStarted, TurnStarted: &protocol.TurnStartedEvent{TurnID: turn1}}),
		contracttest.UserMessage("u1"), contracttest.AssistantMessage("a1"),
		rollout.NewEventMsgItem(protocol.EventMsg{Type: protocol.EventMsgKindTurnComplete, TurnComplete: &protocol.TurnCompleteEvent{TurnID: turn1}}),
	)
	mc, err := testStore.Threads().LoadLatestModelContext(ctx, threadstore.LoadThreadHistoryParams{ThreadID: id})
	if err != nil || len(mc.Items) != 4 {
		t.Fatalf("no-compaction model context = %d items (%v)", len(mc.Items), err)
	}
	mustAppend(t, ctx, id,
		rollout.NewCompactedItem(rollout.CompactedItem{Message: "summary"}),
		rollout.NewEventMsgItem(protocol.EventMsg{Type: protocol.EventMsgKindTurnStarted, TurnStarted: &protocol.TurnStartedEvent{TurnID: turn2}}),
		contracttest.UserMessage("u2"),
	)
	mc, err = testStore.Threads().LoadLatestModelContext(ctx, threadstore.LoadThreadHistoryParams{ThreadID: id})
	if err != nil || len(mc.Items) != 3 || mc.Items[0].Kind != rollout.RolloutItemKindCompacted {
		t.Fatalf("post-compaction model context = %+v (%v)", mc.Items, err)
	}
	hist, err := testStore.Threads().LoadHistory(ctx, threadstore.LoadThreadHistoryParams{ThreadID: id})
	if err != nil || len(hist.Items) != 7 {
		t.Fatalf("full history = %d (%v)", len(hist.Items), err)
	}

	p1, err := testStore.Threads().ListItems(ctx, threadstore.ListItemsParams{ThreadID: id, PageSize: 2})
	if err != nil || len(p1.Items) != 2 || p1.NextCursor == nil {
		t.Fatalf("items page 1 = %+v (%v)", p1, err)
	}
	p2, err := testStore.Threads().ListItems(ctx, threadstore.ListItemsParams{ThreadID: id, PageSize: 2, Cursor: p1.NextCursor})
	if err != nil || len(p2.Items) != 1 || p2.NextCursor != nil {
		t.Fatalf("items page 2 = %+v (%v)", p2, err)
	}
	turns, err := testStore.Threads().ListTurns(ctx, threadstore.ListTurnsParams{ThreadID: id, PageSize: 10})
	if err != nil || len(turns.Turns) != 2 {
		t.Fatalf("turns = %+v (%v)", turns, err)
	}
	fork, err := testStore.Threads().PrepareFork(ctx, threadstore.PrepareForkParams{ThreadID: id})
	if err != nil || fork.HistoryBase == nil || fork.HistoryBase.EndOrdinalExclusive != 8 || len(fork.ModelContext) != 3 {
		t.Fatalf("prepare fork = %+v (%v)", fork, err)
	}
	fork.Release()
}

// TestOpenSearchAndMisc Open/Pool/Close 生命周期；SearchThreads 命中 title / preview 且给 snippet、
// 空 term 拒绝；ArchiveThreads 批量 + Archived 列表；ReadThreadByRolloutPath 显式 Unsupported；
// AppendRolloutItems 与 ThreadStore.AppendItems 共享同一条 seq 流。
func TestOpenSearchAndMisc(t *testing.T) {
	ctx := tenantCtx(t)
	s, err := Open(context.Background(), testPool.Config().ConnString(), Options{})
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	if s.Pool() == nil || s.opts.InlinePayloadLimit != DefaultInlinePayloadLimit || s.opts.DefaultModel != "chat-default" {
		t.Fatalf("defaults = %+v", s.opts)
	}
	defer s.Close()
	ts := s.Threads()

	a := mustCreate(t, ctx, 50)
	b := mustCreate(t, ctx, 51)
	c := mustCreate(t, ctx, 52)
	if _, err := ts.UpdateThreadMetadata(ctx, threadstore.UpdateThreadMetadataParams{ThreadID: a, Patch: threadstore.ThreadMetadataPatch{Title: strp("巡检 数据库 慢查询")}}); err != nil {
		t.Fatalf("patch a: %v", err)
	}
	if _, err := ts.UpdateThreadMetadata(ctx, threadstore.UpdateThreadMetadataParams{ThreadID: b, Patch: threadstore.ThreadMetadataPatch{Preview: strp("聊聊 慢查询 的索引")}}); err != nil {
		t.Fatalf("patch b: %v", err)
	}
	res, err := ts.SearchThreads(ctx, threadstore.SearchThreadsParams{SearchTerm: "慢查询", PageSize: 10})
	if err != nil || len(res.Items) != 2 {
		t.Fatalf("search = %+v (%v)", res, err)
	}
	for _, it := range res.Items {
		switch it.Thread.ThreadID.String() {
		case a.String():
			if it.Snippet != "巡检 数据库 慢查询" {
				t.Fatalf("title snippet = %q", it.Snippet)
			}
		case b.String():
			if it.Snippet != "聊聊 慢查询 的索引" {
				t.Fatalf("preview snippet = %q", it.Snippet)
			}
		default:
			t.Fatalf("unexpected hit %s", it.Thread.ThreadID)
		}
	}
	if _, err := ts.SearchThreads(ctx, threadstore.SearchThreadsParams{SearchTerm: "  "}); err == nil {
		t.Fatal("empty term must be rejected")
	}

	if done, err := ts.ArchiveThreads(ctx, threadstore.ArchiveThreadsParams{ThreadIDs: []protocol.ThreadID{b, c}}); err != nil || len(done) != 2 {
		t.Fatalf("archive threads = %v (%v)", done, err)
	}
	archived, err := ts.ListThreads(ctx, threadstore.ListThreadsParams{Archived: true, PageSize: 10})
	if err != nil || len(archived.Items) != 2 {
		t.Fatalf("archived list = %+v (%v)", archived, err)
	}
	active, err := ts.ListThreads(ctx, threadstore.ListThreadsParams{PageSize: 10})
	if err != nil || len(active.Items) != 1 || active.Items[0].ThreadID.String() != a.String() {
		t.Fatalf("active list = %+v (%v)", active, err)
	}
	if _, err := ts.ReadThreadByRolloutPath(ctx, threadstore.ReadThreadByRolloutPathParams{RolloutPath: "/nowhere/rollout.jsonl"}); err == nil {
		t.Fatal("rollout path lookup must be unsupported")
	}

	last, err := s.AppendRolloutItems(ctx, a, []rollout.RolloutItem{contracttest.UserMessage("via runtime")})
	if err != nil || last != 1 {
		t.Fatalf("append rollout items = %d (%v)", last, err)
	}
	mustAppend(t, ctx, a, contracttest.AssistantMessage("via store"))
	if info, _ := s.GetThreadInfo(ctx, a); info.LastSeq != 2 {
		t.Fatalf("shared seq stream broken: %+v", info)
	}
	if err := s.Threads().DiscardThread(ctx, a); err != nil {
		t.Fatalf("discard: %v", err)
	}
}
