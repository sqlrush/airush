package pgstore

import (
	"encoding/json"
	"math"
	"testing"

	"github.com/sqlrush/codexgo/pkg/threadstore"
)

func TestTurnAggFold(t *testing.T) {
	a := &turnAgg{turn: threadstore.StoredTurn{TurnID: "t1", Status: threadstore.StoredTurnStatusInProgress}}
	a.fold("task_started", nil, 100)
	if a.started == nil || *a.started != 100 || a.turn.Status != threadstore.StoredTurnStatusInProgress {
		t.Fatalf("after start = %+v", a)
	}
	a.fold("error", []byte(`{"payload":{"message":"boom"}}`), 101)
	if a.turn.Status != threadstore.StoredTurnStatusFailed || a.turn.Error == nil || a.turn.Error.Message != "boom" {
		t.Fatalf("after error = %+v", a.turn)
	}
	a.fold("turn_complete", nil, 105)
	if a.turn.Status != threadstore.StoredTurnStatusCompleted || a.completed == nil || *a.completed != 105 {
		t.Fatalf("after complete = %+v", a.turn)
	}
	b := &turnAgg{}
	b.fold("turn_aborted", nil, 7)
	if b.turn.Status != threadstore.StoredTurnStatusInterrupted || b.completed == nil {
		t.Fatalf("after abort = %+v", b.turn)
	}
	b.fold("unknown_event", nil, 8) // 无操作
	if errorMessageOf([]byte(`not json`)) != "" || errorMessageOf([]byte(`{"payload":{"message":""}}`)) != "" || errorMessageOf([]byte(`{"payload":{"message":"m"}}`)) != "m" {
		t.Fatal("errorMessageOf")
	}
	page := pageTurns([]*turnAgg{{turn: threadstore.StoredTurn{TurnID: "a"}, firstSeq: 1, started: int64p(1), completed: int64p(3)}, {turn: threadstore.StoredTurn{TurnID: "b"}, firstSeq: 5}}, 1, "")
	if len(page.Turns) != 1 || page.NextCursor == nil || *page.NextCursor != "1" || page.Turns[0].DurationMS == nil || *page.Turns[0].DurationMS != 2000 || page.Turns[0].ItemsView != threadstore.StoredTurnItemsViewSummary {
		t.Fatalf("page = %+v", page)
	}
}

func int64p(v int64) *int64 { return &v }

func TestSeqOrdinalConversions(t *testing.T) {
	if seqToOrdinal(-5) != 0 || seqToOrdinal(7) != 7 || ordinalToSeq(3) != 3 || ordinalToSeq(math.MaxUint64) != math.MaxInt64 {
		t.Fatal("conversions")
	}
	var raw json.RawMessage = []byte(`{}`)
	if len(raw) == 0 {
		t.Fatal("unreachable")
	}
}
