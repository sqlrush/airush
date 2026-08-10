package repo

import (
	"context"
	"fmt"

	"github.com/jackc/pgx/v5"

	"github.com/sqlrush/airush/libs/apierror"
)

// InsertAlias 为数据源新增别名；唯一冲突 → AR_ALIAS_CONFLICT（mapPgError）。
func InsertAlias(ctx context.Context, tx pgx.Tx, datasourceID, alias, source string) (Alias, error) {
	row := tx.QueryRow(ctx, `INSERT INTO datasource_aliases
		(tenant_id, datasource_id, alias, source)
		VALUES (`+tenantExpr+`, $1, $2, $3)
		RETURNING id, datasource_id, alias, source, created_at`,
		datasourceID, alias, source)
	var a Alias
	if err := row.Scan(&a.ID, &a.DatasourceID, &a.Alias, &a.Source, &a.CreatedAt); err != nil {
		return Alias{}, mapPgError(err)
	}
	return a, nil
}

// ListAliases 列出数据源全部别名（别名量级小，不分页）。
func ListAliases(ctx context.Context, tx pgx.Tx, datasourceID string) ([]Alias, error) {
	rows, err := tx.Query(ctx, `SELECT id, datasource_id, alias, source, created_at
		FROM datasource_aliases WHERE datasource_id = $1 ORDER BY created_at, id`, datasourceID)
	if err != nil {
		return nil, fmt.Errorf("list aliases: %w", err)
	}
	defer rows.Close()

	var items []Alias
	for rows.Next() {
		var a Alias
		if err := rows.Scan(&a.ID, &a.DatasourceID, &a.Alias, &a.Source, &a.CreatedAt); err != nil {
			return nil, fmt.Errorf("scan alias: %w", err)
		}
		items = append(items, a)
	}
	return items, rows.Err()
}

// DeleteAlias 按别名值删除（路径语义 …/aliases/{alias}）。
func DeleteAlias(ctx context.Context, tx pgx.Tx, datasourceID, alias string) error {
	tag, err := tx.Exec(ctx, `DELETE FROM datasource_aliases
		WHERE datasource_id = $1 AND alias = $2`, datasourceID, alias)
	if err != nil {
		return mapPgError(fmt.Errorf("delete alias: %w", err))
	}
	if tag.RowsAffected() == 0 {
		return apierror.New(apierror.CodeCommonNotFound)
	}
	return nil
}
