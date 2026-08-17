package pgstore

import (
	"context"
	"math"
	"strconv"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/sqlrush/codexgo/pkg/threadstore"
)

// ListItems 按 seq keyset 分页返回 response_item 事件（0.147 thread/items/list 语义）：
// item_json 是 rollout 项原文（含截断摘要），updated_at_ordinal = seq。turn_id 过滤靠事件行的
// turn_id（response_item 项无 turn_id 时归入最近的 turn_started 之后——Stage 1 简化为按行 turn_id）。
func (t *ThreadStore) ListItems(ctx context.Context, params threadstore.ListItemsParams) (threadstore.ItemPage, error) {
	size := normalizePageSize(params.PageSize)
	from, err := seqCursor(params.Cursor)
	if err != nil {
		return threadstore.ItemPage{}, err
	}
	var out threadstore.ItemPage
	err = t.s.InTenantTx(ctx, func(ctx context.Context, tx pgx.Tx) error {
		if _, err := t.readRow(ctx, tx, params.ThreadID, params.IncludeArchived); err != nil {
			return err
		}
		q, args := itemsQuery(params, from, size)
		rows, err := tx.Query(ctx, q, args...)
		if err != nil {
			return storeErr(err, "list items for %s", params.ThreadID)
		}
		defer rows.Close()
		page, err := scanItemPage(rows, size)
		if err != nil {
			return storeErr(err, "scan items for %s", params.ThreadID)
		}
		out = page
		return nil
	})
	return out, err
}

// itemsQuery 拼 ListItems 的 SQL（多取一行判断有无下一页）。
func itemsQuery(params threadstore.ListItemsParams, from *int64, size int) (string, []any) {
	order, cmp := "ASC", ">"
	if params.SortDirection == threadstore.SortDirectionDesc {
		order, cmp = "DESC", "<"
	}
	q := `SELECT seq, turn_id, event_type, payload, payload_ref, created_at FROM agent_rollout_events
		WHERE thread_id = $1 AND event_type = $2`
	args := []any{params.ThreadID.String(), EventTypeResponseItem}
	if params.TurnID != nil {
		args = append(args, *params.TurnID)
		q += " AND turn_id = $" + strconv.Itoa(len(args))
	}
	if from != nil {
		args = append(args, *from)
		q += " AND seq " + cmp + " $" + strconv.Itoa(len(args))
	}
	if params.AfterUpdatedAtOrdinal != nil {
		args = append(args, ordinalToSeq(*params.AfterUpdatedAtOrdinal))
		q += " AND seq > $" + strconv.Itoa(len(args))
	}
	args = append(args, size+1)
	q += " ORDER BY seq " + order + " LIMIT $" + strconv.Itoa(len(args))
	return q, args
}

// scanItemPage 把事件行装成 StoredThreadItem 页；第 size+1 行只用来给出游标。
func scanItemPage(rows pgx.Rows, size int) (threadstore.ItemPage, error) {
	var (
		out     threadstore.ItemPage
		lastSeq int64
	)
	for rows.Next() {
		var (
			ev        StoredEvent
			createdAt time.Time
		)
		if err := rows.Scan(&ev.Seq, &ev.TurnID, &ev.EventType, &ev.Payload, &ev.PayloadRef, &createdAt); err != nil {
			return threadstore.ItemPage{}, err
		}
		if len(out.Items) == size {
			c := strconv.FormatInt(lastSeq, 10)
			out.NextCursor = &c
			break
		}
		turnID := ""
		if ev.TurnID != nil {
			turnID = *ev.TurnID
		}
		out.Items = append(out.Items, threadstore.StoredThreadItem{
			TurnID:           turnID,
			ItemID:           strconv.FormatInt(ev.Seq, 10),
			UpdatedAtOrdinal: seqToOrdinal(ev.Seq),
			CreatedAtMS:      createdAt.UnixMilli(),
			ItemJSON:         []byte(ev.Payload),
		})
		lastSeq = ev.Seq
	}
	return out, rows.Err()
}

// seqCursor 解 "seq" 数字游标。
func seqCursor(c *string) (*int64, error) {
	if c == nil || *c == "" {
		return nil, nil
	}
	v, err := strconv.ParseInt(*c, 10, 64)
	if err != nil {
		return nil, threadstore.NewInvalidRequestError("invalid cursor")
	}
	return &v, nil
}

// seqToOrdinal / ordinalToSeq 在 seq（bigint，恒 ≥0）与 0.147 的 uint64 序数间转换，
// 越界值截断而不是回绕（gosec G115）。
func seqToOrdinal(seq int64) uint64 {
	if seq < 0 {
		return 0
	}
	return uint64(seq)
}

func ordinalToSeq(ord uint64) int64 {
	if ord > math.MaxInt64 {
		return math.MaxInt64
	}
	return int64(ord)
}
