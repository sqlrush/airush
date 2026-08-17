package pgstore

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/sqlrush/codexgo/pkg/protocol"
	"github.com/sqlrush/codexgo/pkg/rollout"
	"github.com/sqlrush/codexgo/pkg/threadstore"

	"github.com/sqlrush/airush/libs/apierror"
)

// Event types written to agent_rollout_events (spec-1.8 §3.3 白名单)：
//   - EventMsg 项：event_type = protocol.EventMsg 变体名（snake_case）；
//   - 其它 rollout 项：session_meta / turn_context / response_item / compacted_item。
//
// 未知 EventMsg 变体（无法序列化）显式拒绝（AR_AGENT_EVENT_UNKNOWN）；payload 超过内联上限
// 时截断为 {"truncated":true,...} 摘要并写 payload_ref（Stage 4 指向对象存储；Stage 1 记
// 引用键，原文不再保留——32KB 内联是 event sourcing 的体量护栏，spec §8 Q3）。
const (
	EventTypeSessionMeta  = "session_meta"
	EventTypeTurnContext  = "turn_context"
	EventTypeResponseItem = "response_item"
	EventTypeCompacted    = "compacted_item"
)

// eventTypeOf 给一条 rollout 项定 event_type；未知项报错。
func eventTypeOf(item rollout.RolloutItem) (string, error) {
	switch item.Kind {
	case rollout.RolloutItemKindSessionMeta:
		return EventTypeSessionMeta, nil
	case rollout.RolloutItemKindTurnContext:
		return EventTypeTurnContext, nil
	case rollout.RolloutItemKindResponseItem:
		return EventTypeResponseItem, nil
	case rollout.RolloutItemKindCompacted:
		return EventTypeCompacted, nil
	case rollout.RolloutItemKindEventMsg:
		if item.EventMsg == nil || item.EventMsg.Type == "" || !knownEventMsg(*item.EventMsg) {
			return "", apierror.New(apierror.CodeAgentEventUnknown)
		}
		return string(item.EventMsg.Type), nil
	default:
		return "", apierror.New(apierror.CodeAgentEventUnknown)
	}
}

// knownEventMsg 判定 EventMsg 是否是 codexgo protocol 认识的变体（白名单 = 变体名集合，spec-1.8 §3.3）。
// 不在 airush 侧复制一份变体名清单（会随 0.147 对齐漂移）：protocol 对未知判别符走 forward-compat
// 分支并保留 Raw，已知变体的 Raw 恒为空——一次序列化往返即可判定。
func knownEventMsg(ev protocol.EventMsg) bool {
	raw, err := json.Marshal(ev)
	if err != nil {
		return false
	}
	var back protocol.EventMsg
	if err := json.Unmarshal(raw, &back); err != nil {
		return false
	}
	return len(back.Raw) == 0
}

// turnIDOf 从 EventMsg 项里尽力取 turn id（有 TurnID 字段的变体），供事件表 turn_id 列
// （uuid 列：turn id 是提交 id，runtime 生成的是 UUIDv7；非 uuid 形状的只留在 payload 里）。
func turnIDOf(item rollout.RolloutItem) *string {
	if item.Kind != rollout.RolloutItemKindEventMsg || item.EventMsg == nil {
		return nil
	}
	raw, err := json.Marshal(item.EventMsg)
	if err != nil {
		return nil
	}
	var probe struct {
		TurnID string `json:"turn_id"`
	}
	if json.Unmarshal(raw, &probe) != nil || probe.TurnID == "" {
		return nil
	}
	if _, err := uuid.Parse(probe.TurnID); err != nil {
		return nil
	}
	return &probe.TurnID
}

// encodedEvent 是一条待写入的事件。
type encodedEvent struct {
	eventType  string
	turnID     *string
	payload    []byte
	payloadRef *string
}

// encodeEvent 序列化 rollout 项并按内联上限截断。
func (s *Store) encodeEvent(threadID string, seq int64, item rollout.RolloutItem) (encodedEvent, error) {
	eventType, err := eventTypeOf(item)
	if err != nil {
		return encodedEvent{}, err
	}
	payload, err := json.Marshal(item)
	if err != nil {
		return encodedEvent{}, apierror.Wrap(apierror.CodeAgentEventUnknown, err)
	}
	ev := encodedEvent{eventType: eventType, turnID: turnIDOf(item), payload: payload}
	if len(payload) > s.opts.InlinePayloadLimit {
		ref := fmt.Sprintf("thread/%s/seq/%d", threadID, seq)
		summary, merr := json.Marshal(map[string]any{
			"type":           string(item.Kind),
			"truncated":      true,
			"original_bytes": len(payload),
			"payload_ref":    ref,
			"event_type":     eventType,
		})
		if merr != nil {
			return encodedEvent{}, apierror.Wrap(apierror.CodeAgentEventUnknown, merr)
		}
		ev.payload = summary
		ev.payloadRef = &ref
	}
	return ev, nil
}

// appendEvents 在事务内把 items 追加到线程事件流：seq 从 agent_threads.last_seq 续，
// 同事务更新 last_seq/updated_at。线程不存在（或已 deleted）→ ThreadNotFound。
func (s *Store) appendEvents(ctx context.Context, tx pgx.Tx, threadID protocol.ThreadID, items []rollout.RolloutItem) (lastSeq int64, err error) {
	if len(items) == 0 {
		return s.currentSeq(ctx, tx, threadID)
	}
	var seq int64
	err = tx.QueryRow(ctx, `SELECT last_seq FROM agent_threads WHERE id = $1 AND status <> 'deleted' FOR UPDATE`, threadID.String()).Scan(&seq)
	if errors.Is(err, pgx.ErrNoRows) {
		return 0, threadstore.NewThreadNotFoundError(threadID)
	}
	if err != nil {
		return 0, storeErr(err, "lock thread %s", threadID)
	}
	tenantID := tenantIDFrom(ctx)
	batch := &pgx.Batch{}
	for _, item := range items {
		seq++
		ev, encErr := s.encodeEvent(threadID.String(), seq, item)
		if encErr != nil {
			return 0, encErr
		}
		batch.Queue(`INSERT INTO agent_rollout_events (tenant_id, thread_id, seq, turn_id, event_type, payload, payload_ref)
			VALUES ($1, $2, $3, $4, $5, $6, $7)`, tenantID, threadID.String(), seq, ev.turnID, ev.eventType, ev.payload, ev.payloadRef)
	}
	res := tx.SendBatch(ctx, batch)
	for range items {
		if _, err := res.Exec(); err != nil {
			_ = res.Close()
			return 0, storeErr(err, "append events for %s", threadID)
		}
	}
	if err := res.Close(); err != nil {
		return 0, storeErr(err, "append events for %s", threadID)
	}
	if _, err := tx.Exec(ctx, `UPDATE agent_threads SET last_seq = $2, updated_at = now() WHERE id = $1`, threadID.String(), seq); err != nil {
		return 0, storeErr(err, "advance last_seq for %s", threadID)
	}
	return seq, nil
}

// currentSeq 读线程当前 last_seq。
func (s *Store) currentSeq(ctx context.Context, tx pgx.Tx, threadID protocol.ThreadID) (int64, error) {
	var seq int64
	err := tx.QueryRow(ctx, `SELECT last_seq FROM agent_threads WHERE id = $1 AND status <> 'deleted'`, threadID.String()).Scan(&seq)
	if errors.Is(err, pgx.ErrNoRows) {
		return 0, threadstore.NewThreadNotFoundError(threadID)
	}
	return seq, storeErr(err, "read last_seq for %s", threadID)
}

// StoredEvent 是事件表的一行（SSE 回放 / 分页读取用）。
type StoredEvent struct {
	Seq        int64
	TurnID     *string
	EventType  string
	Payload    json.RawMessage
	PayloadRef *string
}

// loadEvents 按 seq 升序读取 [fromSeq, ∞) 的事件（fromSeq<=0 表示从头），limit<=0 不限。
func (s *Store) loadEvents(ctx context.Context, tx pgx.Tx, threadID protocol.ThreadID, fromSeq int64, limit int) ([]StoredEvent, error) {
	q := `SELECT seq, turn_id, event_type, payload, payload_ref FROM agent_rollout_events
		WHERE thread_id = $1 AND seq >= $2 ORDER BY seq`
	args := []any{threadID.String(), fromSeq}
	if limit > 0 {
		q += " LIMIT $3"
		args = append(args, limit)
	}
	rows, err := tx.Query(ctx, q, args...)
	if err != nil {
		return nil, storeErr(err, "load events for %s", threadID)
	}
	defer rows.Close()
	var out []StoredEvent
	for rows.Next() {
		var ev StoredEvent
		if err := rows.Scan(&ev.Seq, &ev.TurnID, &ev.EventType, &ev.Payload, &ev.PayloadRef); err != nil {
			return nil, storeErr(err, "scan event for %s", threadID)
		}
		out = append(out, ev)
	}
	return out, storeErr(rows.Err(), "iterate events for %s", threadID)
}

// decodeRolloutItems 把事件行还原为 rollout 项；被截断的事件（payload_ref 非空）无法还原
// 原文，按 event_type 还原为一个占位项：response_item → 带说明的 message，其它 → 跳过。
func decodeRolloutItems(events []StoredEvent) []rollout.RolloutItem {
	items := make([]rollout.RolloutItem, 0, len(events))
	for _, ev := range events {
		if ev.PayloadRef != nil {
			if ev.EventType == EventTypeResponseItem {
				items = append(items, rollout.NewResponseItem(truncatedPlaceholder(*ev.PayloadRef)))
			}
			continue
		}
		var item rollout.RolloutItem
		if err := json.Unmarshal(ev.Payload, &item); err != nil {
			// 事件表只接受本进程写入的形状；解不开的行跳过而不是让整段历史不可用。
			continue
		}
		items = append(items, item)
	}
	return items
}

// truncatedPlaceholder 是被截断的工具结果在历史里的占位。
func truncatedPlaceholder(ref string) protocol.ResponseItem {
	return protocol.ResponseItem{
		Type: protocol.ResponseItemKindMessage,
		Role: "developer",
		Content: []protocol.ContentItem{{
			Type: protocol.ContentItemKindInputText,
			Text: fmt.Sprintf("[tool output truncated: exceeded the inline limit; stored at %s]", ref),
		}},
	}
}
