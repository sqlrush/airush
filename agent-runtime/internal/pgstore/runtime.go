package pgstore

import (
	"context"
	"encoding/json"
	"errors"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/sqlrush/codexgo/pkg/protocol"
	"github.com/sqlrush/codexgo/pkg/rollout"

	"github.com/sqlrush/airush/libs/apierror"
	"github.com/sqlrush/airush/libs/tenancy"
)

// runtime-facing operations（不属于 codexgo ThreadStore 契约，是调度器/API 需要的线程状态机、
// 心跳、输入队列与恢复扫描）。

// ThreadStatus 是 agent_threads.status 的取值。
type ThreadStatus string

// 线程状态取值（agent_threads.status CHECK 约束同一集合）。
const (
	ThreadStatusIdle        ThreadStatus = "idle"
	ThreadStatusRunning     ThreadStatus = "running"
	ThreadStatusInterrupted ThreadStatus = "interrupted"
	ThreadStatusArchived    ThreadStatus = "archived"
	ThreadStatusDeleted     ThreadStatus = "deleted"
)

// ThreadInfo 是 runtime 视角的线程行。
type ThreadInfo struct {
	ID          string
	AgentID     *string
	ParentID    *string
	Title       string
	Status      ThreadStatus
	Model       string
	RunningPod  *string
	HeartbeatAt *time.Time
	LastSeq     int64
	CreatedAt   time.Time
	UpdatedAt   time.Time
}

// ThreadAttributes 是 runtime 建线程后补写的属性。
type ThreadAttributes struct {
	AgentID *string
	Model   string
	Title   string
}

// SetThreadAttributes 写 agent_id / model / title（空 model 保留原值）。
func (s *Store) SetThreadAttributes(ctx context.Context, threadID protocol.ThreadID, attrs ThreadAttributes) error {
	return s.InTenantTx(ctx, func(ctx context.Context, tx pgx.Tx) error {
		tag, err := tx.Exec(ctx, `UPDATE agent_threads SET agent_id = COALESCE($2, agent_id), model = COALESCE(NULLIF($3, ''), model),
			title = CASE WHEN $4 <> '' THEN $4 ELSE title END, updated_at = now() WHERE id = $1 AND status <> 'deleted'`,
			threadID.String(), attrs.AgentID, attrs.Model, attrs.Title)
		if err != nil {
			return storeErr(err, "set thread attributes %s", threadID)
		}
		if tag.RowsAffected() == 0 {
			return apierror.New(apierror.CodeAgentThreadNotFound)
		}
		return nil
	})
}

// GetThreadInfo 读线程行（deleted 视为不存在 → AR_AGENT_THREAD_NOT_FOUND）。
func (s *Store) GetThreadInfo(ctx context.Context, threadID protocol.ThreadID) (ThreadInfo, error) {
	var info ThreadInfo
	err := s.InTenantTx(ctx, func(ctx context.Context, tx pgx.Tx) error {
		return tx.QueryRow(ctx, `SELECT id, agent_id, parent_thread_id, title, status, model, running_pod, heartbeat_at, last_seq, created_at, updated_at
			FROM agent_threads WHERE id = $1 AND status <> 'deleted'`, threadID.String()).
			Scan(&info.ID, &info.AgentID, &info.ParentID, &info.Title, &info.Status, &info.Model, &info.RunningPod, &info.HeartbeatAt, &info.LastSeq, &info.CreatedAt, &info.UpdatedAt)
	})
	if errors.Is(err, pgx.ErrNoRows) {
		return ThreadInfo{}, apierror.New(apierror.CodeAgentThreadNotFound)
	}
	return info, storeErr(err, "read thread info %s", threadID)
}

// ClaimTurn 把空闲/中断的线程标为 running（记录 pod 与心跳）；已在运行 → false（会话内串行）。
func (s *Store) ClaimTurn(ctx context.Context, threadID protocol.ThreadID, pod string) (claimed bool, err error) {
	err = s.InTenantTx(ctx, func(ctx context.Context, tx pgx.Tx) error {
		tag, err := tx.Exec(ctx, `UPDATE agent_threads SET status = 'running', running_pod = $2, heartbeat_at = now(), updated_at = now()
			WHERE id = $1 AND status IN ('idle', 'interrupted')`, threadID.String(), pod)
		if err != nil {
			return storeErr(err, "claim turn %s", threadID)
		}
		claimed = tag.RowsAffected() == 1
		if !claimed {
			var status string
			if err := tx.QueryRow(ctx, `SELECT status FROM agent_threads WHERE id = $1 AND status <> 'deleted'`, threadID.String()).Scan(&status); errors.Is(err, pgx.ErrNoRows) {
				return apierror.New(apierror.CodeAgentThreadNotFound)
			}
		}
		return nil
	})
	return claimed, err
}

// Heartbeat 刷新 running 线程的心跳。
func (s *Store) Heartbeat(ctx context.Context, threadID protocol.ThreadID, pod string) error {
	return s.InTenantTx(ctx, func(ctx context.Context, tx pgx.Tx) error {
		_, err := tx.Exec(ctx, `UPDATE agent_threads SET heartbeat_at = now() WHERE id = $1 AND status = 'running' AND running_pod = $2`, threadID.String(), pod)
		return storeErr(err, "heartbeat %s", threadID)
	})
}

// ReleaseTurn 把 running 线程回到 idle（正常结束）或 interrupted（中断/排水）。
func (s *Store) ReleaseTurn(ctx context.Context, threadID protocol.ThreadID, to ThreadStatus) error {
	if to != ThreadStatusIdle && to != ThreadStatusInterrupted {
		return apierror.New(apierror.CodeValidationFailed)
	}
	return s.InTenantTx(ctx, func(ctx context.Context, tx pgx.Tx) error {
		_, err := tx.Exec(ctx, `UPDATE agent_threads SET status = $2, running_pod = NULL, heartbeat_at = NULL, updated_at = now()
			WHERE id = $1 AND status = 'running'`, threadID.String(), string(to))
		return storeErr(err, "release turn %s", threadID)
	})
}

// StaleRunningThread 是恢复扫描命中的孤儿 running 线程。
type StaleRunningThread struct {
	TenantID   string
	ThreadID   protocol.ThreadID
	RunningPod *string
}

// MarkStaleRunningInterrupted 是启动期恢复（spec-1.8 §3.8）：把 status='running' 且心跳早于
// now()-staleAfter 的线程标 interrupted（可 resume，不自动重跑）。
// 不开任何跨租户旁路：先读系统表 tenants（无租户语义，spec-0.6），再逐租户走同一条 RLS
// 事务路径改状态——连接串用户是否超级用户都成立（FORCE RLS 对表 owner 同样生效）。
func (s *Store) MarkStaleRunningInterrupted(ctx context.Context, staleAfter time.Duration) ([]StaleRunningThread, error) {
	tenants, err := s.listTenantIDs(ctx)
	if err != nil {
		return nil, err
	}
	var out []StaleRunningThread
	for _, tenantID := range tenants {
		hits, err := s.markStaleForTenant(tenantCtx(ctx, tenantID), staleAfter)
		if err != nil {
			return nil, err
		}
		out = append(out, hits...)
	}
	return out, nil
}

// tenantCtx 给跨租户扫描的每一步派生租户 ctx（不含请求级取消以外的东西）。
func tenantCtx(ctx context.Context, tenantID string) context.Context {
	return tenancy.WithTenant(ctx, tenantID)
}

// listTenantIDs 读 tenants 主档（系统表，不挂 RLS）。
func (s *Store) listTenantIDs(ctx context.Context) ([]string, error) {
	rows, err := s.pool.Query(ctx, `SELECT id FROM tenants ORDER BY id`)
	if err != nil {
		return nil, storeErr(err, "list tenants")
	}
	defer rows.Close()
	var out []string
	for rows.Next() {
		var id string
		if err := rows.Scan(&id); err != nil {
			return nil, storeErr(err, "scan tenant id")
		}
		out = append(out, id)
	}
	return out, storeErr(rows.Err(), "iterate tenants")
}

// markStaleForTenant 在一个租户事务内把孤儿 running 线程标 interrupted。
func (s *Store) markStaleForTenant(ctx context.Context, staleAfter time.Duration) ([]StaleRunningThread, error) {
	var out []StaleRunningThread
	err := s.InTenantTx(ctx, func(ctx context.Context, tx pgx.Tx) error {
		// RETURNING 给的是更新后的值，先在 CTE 里留住原 running_pod。
		rows, err := tx.Query(ctx, `WITH stale AS (
				SELECT id, running_pod FROM agent_threads
				WHERE status = 'running' AND (heartbeat_at IS NULL OR heartbeat_at < now() - make_interval(secs => $1))
				FOR UPDATE
			)
			UPDATE agent_threads t SET status = 'interrupted', running_pod = NULL, heartbeat_at = NULL, updated_at = now()
			FROM stale WHERE t.id = stale.id
			RETURNING t.tenant_id, t.id, stale.running_pod`, staleAfter.Seconds())
		if err != nil {
			return storeErr(err, "mark stale running threads")
		}
		defer rows.Close()
		for rows.Next() {
			var st StaleRunningThread
			var id string
			if err := rows.Scan(&st.TenantID, &id, &st.RunningPod); err != nil {
				return storeErr(err, "scan stale thread")
			}
			st.ThreadID = protocol.NewThreadID(id)
			out = append(out, st)
		}
		return storeErr(rows.Err(), "iterate stale threads")
	})
	return out, err
}

// EnsureEventPartitions 预建当月与下月的事件分区（幂等；启动期与月切换前调用）。
func (s *Store) EnsureEventPartitions(ctx context.Context) error {
	now := s.clock().UTC()
	for _, m := range []time.Time{now, now.AddDate(0, 1, 0)} {
		first := time.Date(m.Year(), m.Month(), 1, 0, 0, 0, 0, time.UTC)
		if _, err := s.pool.Exec(ctx, `SELECT agent_rollout_events_ensure_partition($1::date)`, first); err != nil {
			return storeErr(err, "ensure event partition for %s", first.Format("2006-01"))
		}
	}
	return nil
}

// ---------------------------------------------------------------------------
// 事件读取（SSE 回放 / API）
// ---------------------------------------------------------------------------

// ReadEvents 读 [fromSeq, ∞) 的事件（fromSeq<=0 从头），limit<=0 不限；线程不存在 → 404。
func (s *Store) ReadEvents(ctx context.Context, threadID protocol.ThreadID, fromSeq int64, limit int) ([]StoredEvent, error) {
	var out []StoredEvent
	err := s.InTenantTx(ctx, func(ctx context.Context, tx pgx.Tx) error {
		if _, err := s.currentSeq(ctx, tx, threadID); err != nil {
			return err
		}
		evs, err := s.loadEvents(ctx, tx, threadID, fromSeq, limit)
		out = evs
		return err
	})
	return out, err
}

// AppendRolloutItems 是给 runtime 自己（审批事件、排水/恢复事件）用的追加入口，与 ThreadStore
// 同一条事件流；返回追加后的 last_seq。
func (s *Store) AppendRolloutItems(ctx context.Context, threadID protocol.ThreadID, items []rollout.RolloutItem) (int64, error) {
	var last int64
	err := s.InTenantTx(ctx, func(ctx context.Context, tx pgx.Tx) error {
		n, err := s.appendEvents(ctx, tx, threadID, items)
		last = n
		return err
	})
	return last, err
}

// ---------------------------------------------------------------------------
// 输入队列（steer / 排队）
// ---------------------------------------------------------------------------

// QueueKind 是 agent_thread_queue.kind。
type QueueKind string

// 队列输入种类（agent_thread_queue.kind CHECK 约束同一集合）。
const (
	QueueKindSteer  QueueKind = "steer"
	QueueKindQueued QueueKind = "queued"
)

// QueuedInput 是队列一行。
type QueuedInput struct {
	ID             string
	ThreadID       protocol.ThreadID
	Kind           QueueKind
	Payload        json.RawMessage
	AdmittedTurnID *string
	CreatedAt      time.Time
}

// EnqueueInput 入队一条输入（payload 是调用方序列化的 protocol.Op / user input）。
func (s *Store) EnqueueInput(ctx context.Context, threadID protocol.ThreadID, id string, kind QueueKind, payload json.RawMessage) error {
	return s.InTenantTx(ctx, func(ctx context.Context, tx pgx.Tx) error {
		if _, err := s.currentSeq(ctx, tx, threadID); err != nil {
			return err
		}
		_, err := tx.Exec(ctx, `INSERT INTO agent_thread_queue (tenant_id, thread_id, id, kind, payload) VALUES ($1, $2, $3, $4, $5)`,
			tenantIDFrom(ctx), threadID.String(), id, string(kind), payload)
		return storeErr(err, "enqueue input for %s", threadID)
	})
}

// PendingInputs 列线程未接纳的输入（按入队顺序）。
func (s *Store) PendingInputs(ctx context.Context, threadID protocol.ThreadID) ([]QueuedInput, error) {
	var out []QueuedInput
	err := s.InTenantTx(ctx, func(ctx context.Context, tx pgx.Tx) error {
		rows, err := tx.Query(ctx, `SELECT id, kind, payload, admitted_turn_id, created_at FROM agent_thread_queue
			WHERE thread_id = $1 AND admitted_turn_id IS NULL ORDER BY created_at, id`, threadID.String())
		if err != nil {
			return storeErr(err, "list pending inputs for %s", threadID)
		}
		defer rows.Close()
		for rows.Next() {
			q := QueuedInput{ThreadID: threadID}
			if err := rows.Scan(&q.ID, &q.Kind, &q.Payload, &q.AdmittedTurnID, &q.CreatedAt); err != nil {
				return storeErr(err, "scan pending input for %s", threadID)
			}
			out = append(out, q)
		}
		return rows.Err()
	})
	return out, err
}

// AdmitInput 记录接纳关系（哪个 turn 接纳了该输入）。
func (s *Store) AdmitInput(ctx context.Context, inputID, turnID string) error {
	return s.InTenantTx(ctx, func(ctx context.Context, tx pgx.Tx) error {
		_, err := tx.Exec(ctx, `UPDATE agent_thread_queue SET admitted_turn_id = $2 WHERE id = $1 AND admitted_turn_id IS NULL`, inputID, turnID)
		return storeErr(err, "admit input %s", inputID)
	})
}

// MarkIdle 把 interrupted 线程置回 idle（ResumeThread：可再被领取）；其它状态无操作。
func (s *Store) MarkIdle(ctx context.Context, threadID protocol.ThreadID) error {
	return s.InTenantTx(ctx, func(ctx context.Context, tx pgx.Tx) error {
		_, err := tx.Exec(ctx, `UPDATE agent_threads SET status = 'idle', running_pod = NULL, heartbeat_at = NULL, updated_at = now()
			WHERE id = $1 AND status = 'interrupted'`, threadID.String())
		return storeErr(err, "mark idle %s", threadID)
	})
}

// DeleteInput 删除一条队列输入（已消费的中断指令 / 无法解析的载荷）。
func (s *Store) DeleteInput(ctx context.Context, inputID string) error {
	return s.InTenantTx(ctx, func(ctx context.Context, tx pgx.Tx) error {
		_, err := tx.Exec(ctx, `DELETE FROM agent_thread_queue WHERE id = $1`, inputID)
		return storeErr(err, "delete input %s", inputID)
	})
}
