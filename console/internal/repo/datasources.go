package repo

import (
	"context"
	"errors"
	"fmt"
	"strings"

	"github.com/jackc/pgx/v5"

	"github.com/sqlrush/airush/libs/apierror"
)

const dsColumns = `id, name, engine_family, engine, engine_version, connect_mode,
	connector_id, (credential_id IS NOT NULL), host, port, database_name,
	group_id, group_role, agent_id, health_status, created_at, updated_at`

// DatasourceInput 是创建数据源的入参（API 层已 schema 校验，此处只负责落库）。
type DatasourceInput struct {
	Name          string
	EngineFamily  string
	Engine        string
	EngineVersion string
	ConnectMode   string
	ConnectorID   *string
	CredentialID  *string
	Host          string
	Port          int
	DatabaseName  string
	GroupID       *string
	GroupRole     *string
	AgentID       *string
}

// DatasourcePatch 是部分更新入参；nil = 不改。GroupID/AgentID 传空串 = 解绑。
type DatasourcePatch struct {
	Name          *string
	Engine        *string
	EngineVersion *string
	Host          *string
	Port          *int
	DatabaseName  *string
	GroupID       *string
	GroupRole     *string
	AgentID       *string
}

// InsertDatasource 落库并返回新行；约束违规经 mapPgError 转错误码。
func InsertDatasource(ctx context.Context, tx pgx.Tx, in DatasourceInput) (Datasource, error) {
	row := tx.QueryRow(ctx, `INSERT INTO datasources
		(tenant_id, name, engine_family, engine, engine_version, connect_mode,
		 connector_id, credential_id, host, port, database_name, group_id, group_role, agent_id)
		VALUES (`+tenantExpr+`, $1, $2, $3, $4, $5, $6, $7, $8, $9, $10, $11, $12, $13)
		RETURNING `+dsColumns,
		in.Name, in.EngineFamily, in.Engine, in.EngineVersion, in.ConnectMode,
		in.ConnectorID, in.CredentialID, in.Host, in.Port, in.DatabaseName,
		in.GroupID, in.GroupRole, in.AgentID)
	ds, err := scanDatasource(row)
	if err != nil {
		return Datasource{}, mapPgError(err)
	}
	return ds, nil
}

// ListDatasources keyset 分页（(created_at, id) 严格序，spec-1.1 §8 Q6）。
func ListDatasources(ctx context.Context, tx pgx.Tx, after *PageCursor, limit int) ([]Datasource, error) {
	rows, err := tx.Query(ctx, `SELECT `+dsColumns+` FROM datasources
		WHERE ($1::timestamptz IS NULL OR (created_at, id) > ($1, $2))
		ORDER BY created_at, id LIMIT $3`, cursorArgs(after, limit)...)
	if err != nil {
		return nil, fmt.Errorf("list datasources: %w", err)
	}
	defer rows.Close()

	var items []Datasource
	for rows.Next() {
		ds, err := scanDatasource(rows)
		if err != nil {
			return nil, fmt.Errorf("scan datasource: %w", err)
		}
		items = append(items, ds)
	}
	return items, rows.Err()
}

// GetDatasource 按 id 取行；查无 → AR_DATASOURCE_NOT_FOUND。
func GetDatasource(ctx context.Context, tx pgx.Tx, id string) (Datasource, error) {
	row := tx.QueryRow(ctx, `SELECT `+dsColumns+` FROM datasources WHERE id = $1`, id)
	ds, err := scanDatasource(row)
	if errors.Is(err, pgx.ErrNoRows) {
		return Datasource{}, apierror.New(apierror.CodeDatasourceNotFound)
	}
	if err != nil {
		return Datasource{}, fmt.Errorf("get datasource: %w", err)
	}
	return ds, nil
}

// UpdateDatasource 按 patch 部分更新并返回新行。
func UpdateDatasource(ctx context.Context, tx pgx.Tx, id string, p DatasourcePatch) (Datasource, error) {
	sets, args := buildDatasourceSets(p)
	if len(sets) == 0 {
		return GetDatasource(ctx, tx, id)
	}
	args = append(args, id)
	row := tx.QueryRow(ctx, `UPDATE datasources SET `+strings.Join(sets, ", ")+
		`, updated_at = now() WHERE id = $`+fmt.Sprint(len(args))+
		` RETURNING `+dsColumns, args...)
	ds, err := scanDatasource(row)
	if errors.Is(err, pgx.ErrNoRows) {
		return Datasource{}, apierror.New(apierror.CodeDatasourceNotFound)
	}
	if err != nil {
		return Datasource{}, mapPgError(err)
	}
	return ds, nil
}

// DeleteDatasource 删除数据源：被组/agent 引用时 409（spec-1.1 §3）；
// 直连凭据随行清除；aliases 由 FK 级联。
func DeleteDatasource(ctx context.Context, tx pgx.Tx, id string) error {
	ds, err := GetDatasource(ctx, tx, id)
	if err != nil {
		return err
	}
	if ds.GroupID != nil || ds.AgentID != nil {
		return apierror.New(apierror.CodeDatasourceInUse).WithDetails(inUseDetails(ds)...)
	}

	var credentialID *string
	err = tx.QueryRow(ctx, `DELETE FROM datasources WHERE id = $1 RETURNING credential_id`, id).
		Scan(&credentialID)
	if err != nil {
		return mapPgError(fmt.Errorf("delete datasource: %w", err))
	}
	if credentialID != nil {
		if _, err := tx.Exec(ctx, `DELETE FROM datasource_credentials WHERE id = $1`, *credentialID); err != nil {
			return fmt.Errorf("delete orphan credential: %w", err)
		}
	}
	return nil
}

func inUseDetails(ds Datasource) []apierror.Detail {
	var details []apierror.Detail
	if ds.GroupID != nil {
		details = append(details, apierror.Detail{Field: "group_id", Reason: "数据源仍在编组中"})
	}
	if ds.AgentID != nil {
		details = append(details, apierror.Detail{Field: "agent_id", Reason: "数据源仍被智能体绑定"})
	}
	return details
}

// buildDatasourceSets 组装 SET 子句；GroupID/AgentID 空串语义为解绑（置 NULL）。
func buildDatasourceSets(p DatasourcePatch) ([]string, []any) {
	var sets []string
	var args []any
	appendSet := func(col string, v any) {
		args = append(args, v)
		sets = append(sets, fmt.Sprintf("%s = $%d", col, len(args)))
	}
	if p.Name != nil {
		appendSet("name", *p.Name)
	}
	if p.Engine != nil {
		appendSet("engine", *p.Engine)
	}
	if p.EngineVersion != nil {
		appendSet("engine_version", *p.EngineVersion)
	}
	if p.Host != nil {
		appendSet("host", *p.Host)
	}
	if p.Port != nil {
		appendSet("port", *p.Port)
	}
	if p.DatabaseName != nil {
		appendSet("database_name", *p.DatabaseName)
	}
	if p.GroupID != nil {
		appendSet("group_id", nullableID(*p.GroupID))
	}
	if p.GroupRole != nil {
		appendSet("group_role", nullableID(*p.GroupRole))
	}
	if p.AgentID != nil {
		appendSet("agent_id", nullableID(*p.AgentID))
	}
	return sets, args
}

// nullableID 空串 → NULL（解绑语义）。
func nullableID(s string) *string {
	if s == "" {
		return nil
	}
	return &s
}

func scanDatasource(row pgx.Row) (Datasource, error) {
	var ds Datasource
	err := row.Scan(&ds.ID, &ds.Name, &ds.EngineFamily, &ds.Engine, &ds.EngineVersion,
		&ds.ConnectMode, &ds.ConnectorID, &ds.HasCredential, &ds.Host, &ds.Port,
		&ds.DatabaseName, &ds.GroupID, &ds.GroupRole, &ds.AgentID, &ds.HealthStatus,
		&ds.CreatedAt, &ds.UpdatedAt)
	return ds, err
}

// cursorArgs 组装 keyset 查询参数（cursor 为 nil 时首页）。
func cursorArgs(after *PageCursor, limit int) []any {
	if after == nil {
		return []any{nil, nil, limit}
	}
	return []any{after.CreatedAt, after.ID, limit}
}
