package repo

import (
	"context"
	"errors"
	"fmt"

	"github.com/jackc/pgx/v5"

	"github.com/sqlrush/airush/libs/apierror"
)

const groupColumns = `id, name, kind, created_at, updated_at`

// GroupInput 创建编组入参。
type GroupInput struct {
	Name string
	Kind string
}

// InsertGroup 落库并返回新行。
func InsertGroup(ctx context.Context, tx pgx.Tx, in GroupInput) (Group, error) {
	row := tx.QueryRow(ctx, `INSERT INTO datasource_groups (tenant_id, name, kind)
		VALUES (`+tenantExpr+`, $1, $2) RETURNING `+groupColumns, in.Name, in.Kind)
	g, err := scanGroup(row)
	if err != nil {
		return Group{}, mapPgError(err)
	}
	return g, nil
}

// ListGroups keyset 分页。
func ListGroups(ctx context.Context, tx pgx.Tx, after *PageCursor, limit int) ([]Group, error) {
	rows, err := tx.Query(ctx, `SELECT `+groupColumns+` FROM datasource_groups
		WHERE ($1::timestamptz IS NULL OR (created_at, id) > ($1, $2))
		ORDER BY created_at, id LIMIT $3`, cursorArgs(after, limit)...)
	if err != nil {
		return nil, fmt.Errorf("list groups: %w", err)
	}
	defer rows.Close()

	var items []Group
	for rows.Next() {
		g, err := scanGroup(rows)
		if err != nil {
			return nil, fmt.Errorf("scan group: %w", err)
		}
		items = append(items, g)
	}
	return items, rows.Err()
}

// GetGroup 按 id；查无 → AR_COMMON_NOT_FOUND（无专属码域）。
func GetGroup(ctx context.Context, tx pgx.Tx, id string) (Group, error) {
	row := tx.QueryRow(ctx, `SELECT `+groupColumns+` FROM datasource_groups WHERE id = $1`, id)
	g, err := scanGroup(row)
	if errors.Is(err, pgx.ErrNoRows) {
		return Group{}, apierror.New(apierror.CodeCommonNotFound)
	}
	if err != nil {
		return Group{}, fmt.Errorf("get group: %w", err)
	}
	return g, nil
}

// RenameGroup 更新名称（kind 定组后不可变——主备/集群语义不同，改 kind = 重建组）。
func RenameGroup(ctx context.Context, tx pgx.Tx, id, name string) (Group, error) {
	row := tx.QueryRow(ctx, `UPDATE datasource_groups SET name = $2, updated_at = now()
		WHERE id = $1 RETURNING `+groupColumns, id, name)
	g, err := scanGroup(row)
	if errors.Is(err, pgx.ErrNoRows) {
		return Group{}, apierror.New(apierror.CodeCommonNotFound)
	}
	if err != nil {
		return Group{}, mapPgError(err)
	}
	return g, nil
}

// DeleteGroup 删除；仍有成员时 409。
func DeleteGroup(ctx context.Context, tx pgx.Tx, id string) error {
	var members int
	if err := tx.QueryRow(ctx,
		`SELECT count(*) FROM datasources WHERE group_id = $1`, id).Scan(&members); err != nil {
		return fmt.Errorf("count group members: %w", err)
	}
	if members > 0 {
		return apierror.New(apierror.CodeCommonConflict).WithDetails(
			apierror.Detail{Field: "group_id", Reason: fmt.Sprintf("编组内仍有 %d 个数据源", members)})
	}
	tag, err := tx.Exec(ctx, `DELETE FROM datasource_groups WHERE id = $1`, id)
	if err != nil {
		return mapPgError(fmt.Errorf("delete group: %w", err))
	}
	if tag.RowsAffected() == 0 {
		return apierror.New(apierror.CodeCommonNotFound)
	}
	return nil
}

func scanGroup(row pgx.Row) (Group, error) {
	var g Group
	err := row.Scan(&g.ID, &g.Name, &g.Kind, &g.CreatedAt, &g.UpdatedAt)
	return g, err
}
