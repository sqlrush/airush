package pgstore

import (
	"context"
	"encoding/json"
	"strconv"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/sqlrush/codexgo/pkg/threadstore"
)

// ListTurns 从 turn_started / turn_complete / turn_aborted / error 事件推导 turn 列表（按首事件
// seq keyset 分页）；Summary 视图不额外装 items（Stage 1：items 经 ListItems 按 turn_id 取）。
func (t *ThreadStore) ListTurns(ctx context.Context, params threadstore.ListTurnsParams) (threadstore.TurnPage, error) {
	size := normalizePageSize(params.PageSize)
	from, err := seqCursor(params.Cursor)
	if err != nil {
		return threadstore.TurnPage{}, err
	}
	var out threadstore.TurnPage
	err = t.s.InTenantTx(ctx, func(ctx context.Context, tx pgx.Tx) error {
		if _, err := t.readRow(ctx, tx, params.ThreadID, params.IncludeArchived); err != nil {
			return err
		}
		q, args := turnsQuery(params, from)
		rows, err := tx.Query(ctx, q, args...)
		if err != nil {
			return storeErr(err, "list turns for %s", params.ThreadID)
		}
		defer rows.Close()
		aggs, err := aggregateTurns(rows)
		if err != nil {
			return storeErr(err, "scan turn events for %s", params.ThreadID)
		}
		out = pageTurns(aggs, size, params.ItemsView)
		return nil
	})
	return out, err
}

// turnsQuery 拼 turn 事件查询（只取带 turn_id 的边界事件）。
func turnsQuery(params threadstore.ListTurnsParams, from *int64) (string, []any) {
	order, cmp := "ASC", ">"
	if params.SortDirection == threadstore.SortDirectionDesc {
		order, cmp = "DESC", "<"
	}
	q := `SELECT seq, turn_id, event_type, payload, created_at FROM agent_rollout_events
		WHERE thread_id = $1 AND turn_id IS NOT NULL
		  AND event_type IN ('turn_started','task_started','turn_complete','task_complete','turn_aborted','error')`
	args := []any{params.ThreadID.String()}
	if from != nil {
		args = append(args, *from)
		q += " AND seq " + cmp + " $" + strconv.Itoa(len(args))
	}
	return q + " ORDER BY seq " + order, args
}

// turnAgg 是一个 turn 的事件折叠状态。
type turnAgg struct {
	turn      threadstore.StoredTurn
	firstSeq  int64
	started   *int64
	completed *int64
}

// aggregateTurns 按出现顺序把边界事件折叠成 turn（返回值保持首事件顺序）。
func aggregateTurns(rows pgx.Rows) ([]*turnAgg, error) {
	var (
		order []*turnAgg
		byID  = map[string]*turnAgg{}
	)
	for rows.Next() {
		var (
			seq       int64
			turnID    string
			eventType string
			payload   []byte
			createdAt time.Time
		)
		if err := rows.Scan(&seq, &turnID, &eventType, &payload, &createdAt); err != nil {
			return nil, err
		}
		agg, ok := byID[turnID]
		if !ok {
			agg = &turnAgg{
				turn:     threadstore.StoredTurn{TurnID: turnID, ItemsView: threadstore.StoredTurnItemsViewNotLoaded, Status: threadstore.StoredTurnStatusInProgress},
				firstSeq: seq,
			}
			byID[turnID] = agg
			order = append(order, agg)
		}
		agg.fold(eventType, payload, createdAt.Unix())
	}
	return order, rows.Err()
}

// fold 把一条边界事件并入 turn 状态。
func (a *turnAgg) fold(eventType string, payload []byte, ts int64) {
	switch eventType {
	case "turn_started", "task_started":
		a.started = &ts
	case "turn_complete", "task_complete":
		a.completed = &ts
		a.turn.Status = threadstore.StoredTurnStatusCompleted
	case "turn_aborted":
		a.completed = &ts
		a.turn.Status = threadstore.StoredTurnStatusInterrupted
	case "error":
		a.turn.Status = threadstore.StoredTurnStatusFailed
		if msg := errorMessageOf(payload); msg != "" {
			a.turn.Error = &threadstore.StoredTurnError{Message: msg}
		}
	}
}

// errorMessageOf 从 error 事件的 rollout 项原文里取 message。
func errorMessageOf(payload []byte) string {
	var env struct {
		Payload json.RawMessage `json:"payload"`
	}
	var probe struct {
		Message string `json:"message"`
	}
	if json.Unmarshal(payload, &env) != nil || json.Unmarshal(env.Payload, &probe) != nil {
		return ""
	}
	return probe.Message
}

// pageTurns 取前 size 个 turn 组页，多出的第一个给出下一页游标。
func pageTurns(aggs []*turnAgg, size int, view threadstore.StoredTurnItemsView) threadstore.TurnPage {
	var out threadstore.TurnPage
	for i, agg := range aggs {
		if len(out.Turns) == size {
			c := strconv.FormatInt(aggs[i-1].firstSeq, 10)
			out.NextCursor = &c
			break
		}
		turn := agg.turn
		turn.StartedAt = agg.started
		turn.CompletedAt = agg.completed
		if agg.started != nil && agg.completed != nil {
			d := (*agg.completed - *agg.started) * 1000
			turn.DurationMS = &d
		}
		if view == "" || view == threadstore.StoredTurnItemsViewSummary {
			turn.ItemsView = threadstore.StoredTurnItemsViewSummary
		}
		out.Turns = append(out.Turns, turn)
	}
	return out
}
