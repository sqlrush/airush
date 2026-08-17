package pgstore

import (
	"context"
	"errors"

	"github.com/jackc/pgx/v5"

	"github.com/sqlrush/airush/libs/apierror"
)

// AgentProfile 是 runtime 需要的 agent 行子集（spec-1.1 agents 表 + spec-1.8 追加的 default_model）。
type AgentProfile struct {
	ID             string
	Name           string
	Kind           string
	Status         string
	InstructionDoc string
	DefaultModel   string
}

// GetAgent 读租户内的 agent（RLS 事务）；不存在 → AR_NOT_FOUND。
func (s *Store) GetAgent(ctx context.Context, agentID string) (AgentProfile, error) {
	var p AgentProfile
	err := s.InTenantTx(ctx, func(ctx context.Context, tx pgx.Tx) error {
		var model *string
		err := tx.QueryRow(ctx, `SELECT id, name, kind, status, instruction_doc, default_model FROM agents WHERE id = $1`, agentID).
			Scan(&p.ID, &p.Name, &p.Kind, &p.Status, &p.InstructionDoc, &model)
		if model != nil {
			p.DefaultModel = *model
		}
		return err
	})
	if errors.Is(err, pgx.ErrNoRows) {
		return AgentProfile{}, apierror.New(apierror.CodeAgentNotFound)
	}
	return p, storeErr(err, "read agent %s", agentID)
}

// PendingThread 是一条"有未接纳输入"的线程（跨租户扫描结果，调度器据此派发）。
type PendingThread struct {
	TenantID string
	ThreadID string
}

// ThreadsWithPendingInputs 列出全部租户里有未接纳输入的线程（每租户一条 RLS 事务，
// 与恢复扫描同一形态；不开跨租户旁路）。
func (s *Store) ThreadsWithPendingInputs(ctx context.Context) ([]PendingThread, error) {
	tenants, err := s.listTenantIDs(ctx)
	if err != nil {
		return nil, err
	}
	var out []PendingThread
	for _, tenantID := range tenants {
		threads, err := s.pendingThreadsForTenant(ctx, tenantID)
		if err != nil {
			return nil, err
		}
		out = append(out, threads...)
	}
	return out, nil
}

func (s *Store) pendingThreadsForTenant(ctx context.Context, tenantID string) ([]PendingThread, error) {
	var out []PendingThread
	err := s.InTenantTx(tenantCtx(ctx, tenantID), func(ctx context.Context, tx pgx.Tx) error {
		rows, err := tx.Query(ctx, `SELECT DISTINCT q.thread_id FROM agent_thread_queue q
			JOIN agent_threads t ON t.tenant_id = q.tenant_id AND t.id = q.thread_id
			WHERE q.admitted_turn_id IS NULL AND t.status <> 'deleted' ORDER BY q.thread_id`)
		if err != nil {
			return storeErr(err, "list pending threads")
		}
		defer rows.Close()
		for rows.Next() {
			var id string
			if err := rows.Scan(&id); err != nil {
				return storeErr(err, "scan pending thread")
			}
			out = append(out, PendingThread{TenantID: tenantID, ThreadID: id})
		}
		return storeErr(rows.Err(), "iterate pending threads")
	})
	return out, err
}
