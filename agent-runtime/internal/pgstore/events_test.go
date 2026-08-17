package pgstore

import (
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

func TestEventTypeOf(t *testing.T) {
	turnID := "01890000-0000-7000-8000-000000000001"
	cases := []struct {
		name    string
		item    rollout.RolloutItem
		want    string
		wantErr bool
	}{
		{"session_meta", rollout.NewSessionMetaItem(rollout.SessionMetaLine{}), EventTypeSessionMeta, false},
		{"turn_context", rollout.NewTurnContextItem(rollout.TurnContextItem{}), EventTypeTurnContext, false},
		{"response_item", contracttest.UserMessage("x"), EventTypeResponseItem, false},
		{"compacted", rollout.NewCompactedItem(rollout.CompactedItem{Message: "m"}), EventTypeCompacted, false},
		{"known event", rollout.NewEventMsgItem(protocol.EventMsg{Type: protocol.EventMsgKindTurnStarted, TurnStarted: &protocol.TurnStartedEvent{TurnID: turnID}}), "task_started", false},
		{"payload-less known event", rollout.NewEventMsgItem(protocol.EventMsg{Type: protocol.EventMsgKindShutdownComplete}), "shutdown_complete", false},
		{"unknown event with raw", rollout.NewEventMsgItem(protocol.EventMsg{Type: "made_up", Raw: json.RawMessage(`{"type":"made_up"}`)}), "", true},
		{"unknown event without raw", rollout.NewEventMsgItem(protocol.EventMsg{Type: "made_up"}), "", true},
		{"empty event", rollout.NewEventMsgItem(protocol.EventMsg{}), "", true},
		{"nil event", rollout.RolloutItem{Kind: rollout.RolloutItemKindEventMsg}, "", true},
		{"unknown kind", rollout.RolloutItem{Kind: "bogus"}, "", true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got, err := eventTypeOf(tc.item)
			if tc.wantErr {
				var ae *apierror.Error
				if !errors.As(err, &ae) || ae.Code != apierror.CodeAgentEventUnknown {
					t.Fatalf("err = %v, want AR_AGENT_EVENT_UNKNOWN", err)
				}
				return
			}
			if err != nil || got != tc.want {
				t.Fatalf("got (%q, %v), want %q", got, err, tc.want)
			}
		})
	}
}

func TestTurnIDOf(t *testing.T) {
	uuidTurn := "01890000-0000-7000-8000-000000000001"
	cases := []struct {
		name string
		item rollout.RolloutItem
		want *string
	}{
		{"uuid turn id", rollout.NewEventMsgItem(protocol.EventMsg{Type: protocol.EventMsgKindTurnStarted, TurnStarted: &protocol.TurnStartedEvent{TurnID: uuidTurn}}), &uuidTurn},
		{"non-uuid turn id stays in payload only", rollout.NewEventMsgItem(protocol.EventMsg{Type: protocol.EventMsgKindTurnStarted, TurnStarted: &protocol.TurnStartedEvent{TurnID: "sub-1"}}), nil},
		{"event without turn id", rollout.NewEventMsgItem(protocol.EventMsg{Type: protocol.EventMsgKindAgentMessage, AgentMessage: &protocol.AgentMessageEvent{Message: "m"}}), nil},
		{"non-event item", contracttest.UserMessage("x"), nil},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := turnIDOf(tc.item)
			switch {
			case tc.want == nil && got != nil:
				t.Fatalf("got %q, want nil", *got)
			case tc.want != nil && (got == nil || *got != *tc.want):
				t.Fatalf("got %v, want %q", got, *tc.want)
			}
		})
	}
}

func TestEncodeEventTruncation(t *testing.T) {
	s := &Store{opts: Options{InlinePayloadLimit: 256}}
	small, err := s.encodeEvent("tid", 1, contracttest.UserMessage("short"))
	if err != nil || small.payloadRef != nil || small.eventType != EventTypeResponseItem {
		t.Fatalf("small = %+v (%v)", small, err)
	}
	big, err := s.encodeEvent("tid", 7, contracttest.AssistantMessage(strings.Repeat("y", 1024)))
	if err != nil {
		t.Fatalf("big: %v", err)
	}
	if big.payloadRef == nil || *big.payloadRef != "thread/tid/seq/7" || len(big.payload) > 256 {
		t.Fatalf("big = %+v", big)
	}
	var summary map[string]any
	if err := json.Unmarshal(big.payload, &summary); err != nil || summary["truncated"] != true || summary["event_type"] != EventTypeResponseItem {
		t.Fatalf("summary = %v (%v)", summary, err)
	}
	if _, err := s.encodeEvent("tid", 8, rollout.NewEventMsgItem(protocol.EventMsg{Type: "nope"})); err == nil {
		t.Fatal("unknown event must fail to encode")
	}
}

func TestDecodeRolloutItems(t *testing.T) {
	item := contracttest.UserMessage("hello")
	raw, _ := json.Marshal(item)
	ref := "thread/x/seq/2"
	events := []StoredEvent{
		{Seq: 1, EventType: EventTypeResponseItem, Payload: raw},
		{Seq: 2, EventType: EventTypeResponseItem, Payload: json.RawMessage(`{"truncated":true}`), PayloadRef: &ref},
		{Seq: 3, EventType: "task_started", Payload: json.RawMessage(`{"truncated":true}`), PayloadRef: &ref},
		{Seq: 4, EventType: EventTypeResponseItem, Payload: json.RawMessage(`not json`)},
	}
	items := decodeRolloutItems(events)
	if len(items) != 2 {
		t.Fatalf("items = %d, want 2 (原文 + 截断占位；截断的非 response_item 与坏行跳过)", len(items))
	}
	if items[0].ResponseItem == nil || items[0].ResponseItem.Content[0].Text != "hello" {
		t.Fatalf("item 0 = %+v", items[0])
	}
	if items[1].ResponseItem == nil || items[1].ResponseItem.Role != "developer" || !strings.Contains(items[1].ResponseItem.Content[0].Text, ref) {
		t.Fatalf("placeholder = %+v", items[1])
	}
}

func TestCursors(t *testing.T) {
	c := listCursor{ID: "abc"}
	enc := encodeCursor(c)
	dec, err := decodeCursor(&enc)
	if err != nil || dec == nil || dec.ID != "abc" {
		t.Fatalf("round trip = %+v (%v)", dec, err)
	}
	if got, err := decodeCursor(nil); err != nil || got != nil {
		t.Fatalf("nil cursor = %v (%v)", got, err)
	}
	bad := "!!!"
	if _, err := decodeCursor(&bad); err == nil {
		t.Fatal("bad base64 must be rejected")
	}
	notJSON := "bm90IGpzb24"
	if _, err := decodeCursor(&notJSON); err == nil {
		t.Fatal("bad json must be rejected")
	}
	seq := "42"
	if v, err := seqCursor(&seq); err != nil || v == nil || *v != 42 {
		t.Fatalf("seq cursor = %v (%v)", v, err)
	}
	empty := ""
	if v, err := seqCursor(&empty); err != nil || v != nil {
		t.Fatalf("empty seq cursor = %v (%v)", v, err)
	}
	if _, err := seqCursor(&bad); err == nil {
		t.Fatal("bad seq cursor must be rejected")
	}
	for n, want := range map[int]int{0: 25, -1: 25, 10: 10, 500: 200} {
		if got := normalizePageSize(n); got != want {
			t.Fatalf("normalizePageSize(%d) = %d, want %d", n, got, want)
		}
	}
}

func TestStoreErr(t *testing.T) {
	if storeErr(nil, "x") != nil {
		t.Fatal("nil passes through")
	}
	tnf := threadstore.NewThreadNotFoundError(contracttest.ThreadID(1))
	if !errors.Is(storeErr(tnf, "wrap"), tnf) {
		t.Fatal("threadstore errors pass through untouched")
	}
	missing := apierror.New(apierror.CodeTenantContextMissing)
	if !errors.Is(storeErr(missing, "wrap"), missing) {
		t.Fatal("tenant-context-missing passes through untouched")
	}
	wrapped := storeErr(errors.New("boom"), "doing %s", "thing")
	var te *threadstore.Error
	if !errors.As(wrapped, &te) || te.Kind != threadstore.ErrorKindInternal || !strings.Contains(te.Message, "doing thing") {
		t.Fatalf("wrapped = %v", wrapped)
	}
}
