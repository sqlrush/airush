package pgstore

import (
	"context"
	"strings"

	"github.com/jackc/pgx/v5"
	"github.com/sqlrush/codexgo/pkg/agentgraph"
	"github.com/sqlrush/codexgo/pkg/protocol"
)

// GraphStore 是 codexgo agentgraph.AgentGraphStore 的 PG 实现（agent_graph_edges）：
// 一个子线程只有一个父（upsert 换父），status open/closed，后代查询用递归 CTE 广度优先。
type GraphStore struct {
	s *Store
}

var _ agentgraph.AgentGraphStore = (*GraphStore)(nil)

// Graph 返回 GraphStore 视图。
func (s *Store) Graph() *GraphStore { return &GraphStore{s: s} }

// UpsertThreadSpawnEdge 写/换 parent→child 边（换父时删旧边）。
func (g *GraphStore) UpsertThreadSpawnEdge(ctx context.Context, parent, child protocol.ThreadID, status agentgraph.ThreadSpawnEdgeStatus) error {
	if !status.IsValid() {
		return agentgraph.NewInvalidRequestError("invalid spawn edge status %q", status)
	}
	return g.s.InTenantTx(ctx, func(ctx context.Context, tx pgx.Tx) error {
		if _, err := tx.Exec(ctx, `DELETE FROM agent_graph_edges WHERE child_thread_id = $1 AND parent_thread_id <> $2`,
			child.String(), parent.String()); err != nil {
			return agentgraph.NewInternalError(err, "replace spawn edge for %s", child)
		}
		_, err := tx.Exec(ctx, `INSERT INTO agent_graph_edges (tenant_id, parent_thread_id, child_thread_id, status)
			VALUES ($1, $2, $3, $4)
			ON CONFLICT (tenant_id, parent_thread_id, child_thread_id) DO UPDATE SET status = EXCLUDED.status`,
			tenantIDFrom(ctx), parent.String(), child.String(), status.String())
		if err != nil {
			return agentgraph.NewInternalError(err, "upsert spawn edge %s→%s", parent, child)
		}
		return nil
	})
}

// SetThreadSpawnEdgeStatus 改子线程边的状态；无边时无操作。
func (g *GraphStore) SetThreadSpawnEdgeStatus(ctx context.Context, child protocol.ThreadID, status agentgraph.ThreadSpawnEdgeStatus) error {
	if !status.IsValid() {
		return agentgraph.NewInvalidRequestError("invalid spawn edge status %q", status)
	}
	return g.s.InTenantTx(ctx, func(ctx context.Context, tx pgx.Tx) error {
		if _, err := tx.Exec(ctx, `UPDATE agent_graph_edges SET status = $2 WHERE child_thread_id = $1`, child.String(), status.String()); err != nil {
			return agentgraph.NewInternalError(err, "set spawn edge status for %s", child)
		}
		return nil
	})
}

// ListThreadSpawnChildren 列直接子线程（按 child id 排序），可按 status 过滤。
func (g *GraphStore) ListThreadSpawnChildren(ctx context.Context, parent protocol.ThreadID, status *agentgraph.ThreadSpawnEdgeStatus) ([]protocol.ThreadID, error) {
	var out []protocol.ThreadID
	err := g.s.InTenantTx(ctx, func(ctx context.Context, tx pgx.Tx) error {
		q := `SELECT child_thread_id FROM agent_graph_edges WHERE parent_thread_id = $1`
		args := []any{parent.String()}
		if status != nil {
			args = append(args, status.String())
			q += " AND status = $2"
		}
		q += " ORDER BY child_thread_id"
		rows, err := tx.Query(ctx, q, args...)
		if err != nil {
			return agentgraph.NewInternalError(err, "list spawn children of %s", parent)
		}
		defer rows.Close()
		for rows.Next() {
			var id string
			if err := rows.Scan(&id); err != nil {
				return agentgraph.NewInternalError(err, "scan spawn child of %s", parent)
			}
			out = append(out, protocol.NewThreadID(id))
		}
		return rows.Err()
	})
	return out, err
}

// ListThreadSpawnDescendants 广度优先列全部后代（每层按 child id 排序）；status 过滤作用于遍历本身
// （只沿匹配状态的边下探，与 in-memory / SQLite 实现一致），不是对全集事后筛选。
func (g *GraphStore) ListThreadSpawnDescendants(ctx context.Context, root protocol.ThreadID, status *agentgraph.ThreadSpawnEdgeStatus) ([]protocol.ThreadID, error) {
	var out []protocol.ThreadID
	err := g.s.InTenantTx(ctx, func(ctx context.Context, tx pgx.Tx) error {
		cond := ""
		args := []any{root.String()}
		if status != nil {
			args = append(args, status.String())
			cond = " AND status = $2"
		}
		q := `WITH RECURSIVE tree AS (
				SELECT child_thread_id, 1 AS depth FROM agent_graph_edges WHERE parent_thread_id = $1` + cond + `
				UNION ALL
				SELECT e.child_thread_id, t.depth + 1 FROM agent_graph_edges e JOIN tree t ON e.parent_thread_id = t.child_thread_id` +
			strings.ReplaceAll(cond, "status", "e.status") + `
			)
			SELECT child_thread_id FROM tree ORDER BY depth, child_thread_id`
		rows, err := tx.Query(ctx, q, args...)
		if err != nil {
			return agentgraph.NewInternalError(err, "list spawn descendants of %s", root)
		}
		defer rows.Close()
		for rows.Next() {
			var id string
			if err := rows.Scan(&id); err != nil {
				return agentgraph.NewInternalError(err, "scan spawn descendant of %s", root)
			}
			out = append(out, protocol.NewThreadID(id))
		}
		return rows.Err()
	})
	return out, err
}
