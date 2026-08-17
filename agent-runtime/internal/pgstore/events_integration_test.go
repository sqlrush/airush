//go:build integration

package pgstore

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"testing"

	"github.com/sqlrush/codexgo/pkg/protocol"
	"github.com/sqlrush/codexgo/pkg/rollout"
	"github.com/sqlrush/codexgo/pkg/threadstore"
	"github.com/sqlrush/codexgo/pkg/threadstore/contracttest"

	"github.com/sqlrush/airush/libs/apierror"
)

// mustCreate 在 ctx 租户下建一条线程并返回 id。
func mustCreate(t *testing.T, ctx context.Context, n int) protocol.ThreadID {
	t.Helper()
	id := contracttest.ThreadID(n)
	if err := testStore.Threads().CreateThread(ctx, contracttest.DefaultCreateParams(t, id)); err != nil {
		t.Fatalf("create thread: %v", err)
	}
	return id
}

func mustAppend(t *testing.T, ctx context.Context, id protocol.ThreadID, items ...rollout.RolloutItem) {
	t.Helper()
	if err := testStore.Threads().AppendItems(ctx, threadstore.AppendThreadItemsParams{ThreadID: id, Items: items}); err != nil {
		t.Fatalf("append: %v", err)
	}
}

// TestEventWhitelist spec-1.8 T3（前半）：event_type 只接受 codexgo protocol 认识的 EventMsg 变体名
// 与四种非事件 rollout 项；未知变体（forward-compat Raw 保留的）与空类型显式拒绝 AR_AGENT_EVENT_UNKNOWN，
// 且整批回滚（seq 不推进）。
func TestEventWhitelist(t *testing.T) {
	ctx := tenantCtx(t)
	id := mustCreate(t, ctx, 1)

	turnID := "01890000-0000-7000-8000-000000000001"
	known := []rollout.RolloutItem{
		rollout.NewEventMsgItem(protocol.EventMsg{Type: protocol.EventMsgKindTurnStarted, TurnStarted: &protocol.TurnStartedEvent{TurnID: turnID}}),
		rollout.NewEventMsgItem(protocol.EventMsg{Type: protocol.EventMsgKindAgentMessage, AgentMessage: &protocol.AgentMessageEvent{Message: "hi"}}),
		rollout.NewCompactedItem(rollout.CompactedItem{Message: "compacted"}),
		contracttest.UserMessage("hello"),
	}
	mustAppend(t, ctx, id, known...)

	events, err := testStore.ReadEvents(ctx, id, 0, 0)
	if err != nil {
		t.Fatalf("read events: %v", err)
	}
	wantTypes := []string{"task_started", "agent_message", EventTypeCompacted, EventTypeResponseItem}
	if len(events) != len(wantTypes) {
		t.Fatalf("events = %d, want %d", len(events), len(wantTypes))
	}
	for i, ev := range events {
		if ev.EventType != wantTypes[i] || ev.Seq != int64(i+1) {
			t.Fatalf("event %d = (%s, seq %d), want (%s, seq %d)", i, ev.EventType, ev.Seq, wantTypes[i], i+1)
		}
	}
	if events[0].TurnID == nil || *events[0].TurnID != turnID {
		t.Fatalf("turn_id column not populated from task_started: %v", events[0].TurnID)
	}

	unknown := []rollout.RolloutItem{
		contracttest.UserMessage("valid"),
		rollout.NewEventMsgItem(protocol.EventMsg{Type: "made_up_kind", Raw: json.RawMessage(`{"type":"made_up_kind","x":1}`)}),
	}
	err = testStore.Threads().AppendItems(ctx, threadstore.AppendThreadItemsParams{ThreadID: id, Items: unknown})
	assertCode(t, err, apierror.CodeAgentEventUnknown)
	err = testStore.Threads().AppendItems(ctx, threadstore.AppendThreadItemsParams{ThreadID: id, Items: []rollout.RolloutItem{rollout.NewEventMsgItem(protocol.EventMsg{})}})
	assertCode(t, err, apierror.CodeAgentEventUnknown)

	info, err := testStore.GetThreadInfo(ctx, id)
	if err != nil {
		t.Fatalf("thread info: %v", err)
	}
	if info.LastSeq != int64(len(known)) {
		t.Fatalf("rejected batch advanced last_seq: %d", info.LastSeq)
	}
}

// TestEventTruncation spec-1.8 T3（后半）：payload 超过内联上限的事件被截断为摘要 + payload_ref，
// 其余事件原样；LoadHistory 对截断的 response_item 给出占位消息，不让整段历史不可用。
func TestEventTruncation(t *testing.T) {
	ctx := tenantCtx(t)
	id := mustCreate(t, ctx, 2)

	big := contracttest.AssistantMessage(strings.Repeat("x", testInlineLimit*2))
	mustAppend(t, ctx, id, contracttest.UserMessage("q"), big, contracttest.UserMessage("q2"))

	events, err := testStore.ReadEvents(ctx, id, 0, 0)
	if err != nil {
		t.Fatalf("read events: %v", err)
	}
	if len(events) != 3 {
		t.Fatalf("events = %d", len(events))
	}
	if events[0].PayloadRef != nil || events[2].PayloadRef != nil {
		t.Fatalf("small events must not be truncated")
	}
	tr := events[1]
	if tr.PayloadRef == nil || *tr.PayloadRef != "thread/"+id.String()+"/seq/2" {
		t.Fatalf("payload_ref = %v", tr.PayloadRef)
	}
	var summary struct {
		Truncated     bool   `json:"truncated"`
		OriginalBytes int    `json:"original_bytes"`
		PayloadRef    string `json:"payload_ref"`
		EventType     string `json:"event_type"`
	}
	if err := json.Unmarshal(tr.Payload, &summary); err != nil {
		t.Fatalf("summary json: %v", err)
	}
	if !summary.Truncated || summary.OriginalBytes <= testInlineLimit || summary.PayloadRef != *tr.PayloadRef || summary.EventType != EventTypeResponseItem {
		t.Fatalf("summary = %+v", summary)
	}
	if len(tr.Payload) > testInlineLimit {
		t.Fatalf("truncated payload still %d bytes", len(tr.Payload))
	}

	hist, err := testStore.Threads().LoadHistory(ctx, threadstore.LoadThreadHistoryParams{ThreadID: id})
	if err != nil {
		t.Fatalf("load history: %v", err)
	}
	if len(hist.Items) != 3 {
		t.Fatalf("history items = %d", len(hist.Items))
	}
	ph := hist.Items[1]
	if ph.Kind != rollout.RolloutItemKindResponseItem || ph.ResponseItem == nil || ph.ResponseItem.Role != "developer" ||
		!strings.Contains(ph.ResponseItem.Content[0].Text, *tr.PayloadRef) {
		t.Fatalf("placeholder = %+v", ph)
	}
}

// TestTenantIsolation AD-10 四项：另一租户对本租户线程 读不到 / 追加不了 / 列表不含 / 删不掉；
// 事件流同样不可见。ctx 无租户 → fail-closed（AR_TENANT_CONTEXT_MISSING）。
func TestTenantIsolation(t *testing.T) {
	ctxA := tenantCtx(t)
	ctxB := otherTenantCtx(t)
	id := mustCreate(t, ctxA, 3)
	mustAppend(t, ctxA, id, contracttest.UserMessage("secret"))

	if _, err := testStore.Threads().ReadThread(ctxB, threadstore.ReadThreadParams{ThreadID: id}); !isThreadNotFound(err) {
		t.Fatalf("read across tenants: %v", err)
	}
	err := testStore.Threads().AppendItems(ctxB, threadstore.AppendThreadItemsParams{ThreadID: id, Items: []rollout.RolloutItem{contracttest.UserMessage("x")}})
	if !isThreadNotFound(err) {
		t.Fatalf("append across tenants: %v", err)
	}
	page, err := testStore.Threads().ListThreads(ctxB, threadstore.ListThreadsParams{PageSize: 100})
	if err != nil {
		t.Fatalf("list: %v", err)
	}
	for _, th := range page.Items {
		if th.ThreadID.String() == id.String() {
			t.Fatalf("thread visible across tenants in list")
		}
	}
	err = testStore.Threads().DeleteThread(ctxB, threadstore.DeleteThreadParams{ThreadID: id})
	if !isThreadNotFound(err) {
		t.Fatalf("delete across tenants: %v", err)
	}
	if _, err := testStore.ReadEvents(ctxB, id, 0, 0); !isThreadNotFound(err) {
		t.Fatalf("events across tenants: %v", err)
	}
	if _, err := testStore.GetThreadInfo(ctxB, id); !isCode(err, apierror.CodeAgentThreadNotFound) {
		t.Fatalf("thread info across tenants: %v", err)
	}
	// 同租户仍完整可见
	if _, err := testStore.Threads().ReadThread(ctxA, threadstore.ReadThreadParams{ThreadID: id}); err != nil {
		t.Fatalf("own read: %v", err)
	}
	// 无租户 ctx → fail-closed
	_, err = testStore.Threads().ReadThread(context.Background(), threadstore.ReadThreadParams{ThreadID: id})
	assertCode(t, err, apierror.CodeTenantContextMissing)
}

// isThreadNotFound 判定 codexgo threadstore 的 ThreadNotFound 错误种类。
func isThreadNotFound(err error) bool {
	var te *threadstore.Error
	return errors.As(err, &te) && te.Kind == threadstore.ErrorKindThreadNotFound
}

func assertCode(t *testing.T, err error, code apierror.Code) {
	t.Helper()
	if !isCode(err, code) {
		t.Fatalf("err = %v, want code %s", err, code)
	}
}

func isCode(err error, code apierror.Code) bool {
	var ae *apierror.Error
	return errors.As(err, &ae) && ae.Code == code
}
