package repo

import (
	"context"
	"errors"
	"fmt"

	"github.com/jackc/pgx/v5"

	"github.com/sqlrush/airush/libs/apierror"
)

const connectorColumns = `id, name, location, version, status, last_heartbeat_at,
	cert_fingerprint, created_at, updated_at`

// 本 spec 只读（展示面）；注册/心跳写路径归 spec-1.2 Connector 协议。

// ListConnectors keyset 分页。
func ListConnectors(ctx context.Context, tx pgx.Tx, after *PageCursor, limit int) ([]Connector, error) {
	rows, err := tx.Query(ctx, `SELECT `+connectorColumns+` FROM connectors
		WHERE ($1::timestamptz IS NULL OR (created_at, id) > ($1, $2))
		ORDER BY created_at, id LIMIT $3`, cursorArgs(after, limit)...)
	if err != nil {
		return nil, fmt.Errorf("list connectors: %w", err)
	}
	defer rows.Close()

	var items []Connector
	for rows.Next() {
		c, err := scanConnector(rows)
		if err != nil {
			return nil, fmt.Errorf("scan connector: %w", err)
		}
		items = append(items, c)
	}
	return items, rows.Err()
}

// GetConnector 按 id；查无 → AR_COMMON_NOT_FOUND。
func GetConnector(ctx context.Context, tx pgx.Tx, id string) (Connector, error) {
	row := tx.QueryRow(ctx, `SELECT `+connectorColumns+` FROM connectors WHERE id = $1`, id)
	c, err := scanConnector(row)
	if errors.Is(err, pgx.ErrNoRows) {
		return Connector{}, apierror.New(apierror.CodeCommonNotFound)
	}
	if err != nil {
		return Connector{}, fmt.Errorf("get connector: %w", err)
	}
	return c, nil
}

func scanConnector(row pgx.Row) (Connector, error) {
	var c Connector
	err := row.Scan(&c.ID, &c.Name, &c.Location, &c.Version, &c.Status,
		&c.LastHeartbeatAt, &c.CertFingerprint, &c.CreatedAt, &c.UpdatedAt)
	return c, err
}
