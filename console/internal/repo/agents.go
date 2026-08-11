package repo

import (
	"context"
	"errors"
	"fmt"
	"strings"

	"github.com/jackc/pgx/v5"

	"github.com/sqlrush/airush/libs/apierror"
)

const agentColumns = `id, name, kind, status, instruction_doc, instruction_version,
	created_at, updated_at`

// AgentInput 创建智能体入参。
type AgentInput struct {
	Name           string
	Kind           string
	InstructionDoc string
}

// AgentPatch 部分更新；InstructionDoc 变更时版本号自增。
type AgentPatch struct {
	Name           *string
	Status         *string
	InstructionDoc *string
}

// InsertAgent 落库并返回新行。
func InsertAgent(ctx context.Context, tx pgx.Tx, in AgentInput) (Agent, error) {
	row := tx.QueryRow(ctx, `INSERT INTO agents (tenant_id, name, kind, instruction_doc)
		VALUES (`+tenantExpr+`, $1, $2, $3) RETURNING `+agentColumns,
		in.Name, in.Kind, in.InstructionDoc)
	a, err := scanAgent(row)
	if err != nil {
		return Agent{}, mapPgError(err)
	}
	return a, nil
}

// ListAgents keyset 分页。
func ListAgents(ctx context.Context, tx pgx.Tx, after *PageCursor, limit int) ([]Agent, error) {
	rows, err := tx.Query(ctx, `SELECT `+agentColumns+` FROM agents
		WHERE ($1::timestamptz IS NULL OR (created_at, id) > ($1, $2))
		ORDER BY created_at, id LIMIT $3`, cursorArgs(after, limit)...)
	if err != nil {
		return nil, fmt.Errorf("list agents: %w", err)
	}
	defer rows.Close()

	var items []Agent
	for rows.Next() {
		a, err := scanAgent(rows)
		if err != nil {
			return nil, fmt.Errorf("scan agent: %w", err)
		}
		items = append(items, a)
	}
	return items, rows.Err()
}

// GetAgent 按 id；查无 → AR_AGENT_NOT_FOUND。
func GetAgent(ctx context.Context, tx pgx.Tx, id string) (Agent, error) {
	row := tx.QueryRow(ctx, `SELECT `+agentColumns+` FROM agents WHERE id = $1`, id)
	a, err := scanAgent(row)
	if errors.Is(err, pgx.ErrNoRows) {
		return Agent{}, apierror.New(apierror.CodeAgentNotFound)
	}
	if err != nil {
		return Agent{}, fmt.Errorf("get agent: %w", err)
	}
	return a, nil
}

// UpdateAgent 部分更新；instruction_doc 提供即版本 +1（内容审计线索，spec-1.1 §2.1）。
func UpdateAgent(ctx context.Context, tx pgx.Tx, id string, p AgentPatch) (Agent, error) {
	var sets []string
	var args []any
	appendSet := func(expr string, v any) {
		args = append(args, v)
		sets = append(sets, fmt.Sprintf(expr, len(args)))
	}
	if p.Name != nil {
		appendSet("name = $%d", *p.Name)
	}
	if p.Status != nil {
		appendSet("status = $%d", *p.Status)
	}
	if p.InstructionDoc != nil {
		appendSet("instruction_doc = $%d", *p.InstructionDoc)
		sets = append(sets, "instruction_version = instruction_version + 1")
	}
	if len(sets) == 0 {
		return GetAgent(ctx, tx, id)
	}
	args = append(args, id)
	row := tx.QueryRow(ctx, `UPDATE agents SET `+strings.Join(sets, ", ")+
		`, updated_at = now() WHERE id = $`+fmt.Sprint(len(args))+
		` RETURNING `+agentColumns, args...)
	a, err := scanAgent(row)
	if errors.Is(err, pgx.ErrNoRows) {
		return Agent{}, apierror.New(apierror.CodeAgentNotFound)
	}
	if err != nil {
		return Agent{}, mapPgError(err)
	}
	return a, nil
}

// DeleteAgent 删除；仍有数据源绑定时 409。
func DeleteAgent(ctx context.Context, tx pgx.Tx, id string) error {
	var bound int
	if err := tx.QueryRow(ctx,
		`SELECT count(*) FROM datasources WHERE agent_id = $1`, id).Scan(&bound); err != nil {
		return fmt.Errorf("count agent bindings: %w", err)
	}
	if bound > 0 {
		return apierror.New(apierror.CodeCommonConflict).WithDetails(
			apierror.Detail{Field: "agent_id", Reason: fmt.Sprintf("仍有 %d 个数据源绑定该智能体", bound)})
	}
	tag, err := tx.Exec(ctx, `DELETE FROM agents WHERE id = $1`, id)
	if err != nil {
		return mapPgError(fmt.Errorf("delete agent: %w", err))
	}
	if tag.RowsAffected() == 0 {
		return apierror.New(apierror.CodeAgentNotFound)
	}
	return nil
}

func scanAgent(row pgx.Row) (Agent, error) {
	var a Agent
	err := row.Scan(&a.ID, &a.Name, &a.Kind, &a.Status, &a.InstructionDoc,
		&a.InstructionVersion, &a.CreatedAt, &a.UpdatedAt)
	return a, err
}
