package repo

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/jackc/pgx/v5"

	"github.com/sqlrush/airush/libs/apierror"
)

const connectorColumns = `id, name, location, version, status, last_heartbeat_at,
	cert_fingerprint, created_at, updated_at`

// 读 API 归 spec-1.1；写路径（注册/心跳/吊销）随 spec-1.2 实装如下。

// ConnectorInput 创建接入器入参。
type ConnectorInput struct {
	Name     string
	Location string
}

// InsertConnector 建实体（status=pending）；注册令牌由 SetEnrollToken 同事务落哈希
// （令牌串内嵌 connector_id，须先取回 id 再生成）。
func InsertConnector(ctx context.Context, tx pgx.Tx, in ConnectorInput) (Connector, error) {
	row := tx.QueryRow(ctx, `INSERT INTO connectors (tenant_id, name, location)
		VALUES (`+tenantExpr+`, $1, $2)
		RETURNING `+connectorColumns, in.Name, in.Location)
	c, err := scanConnector(row)
	if err != nil {
		return Connector{}, mapPgError(err)
	}
	return c, nil
}

// SetEnrollToken 落注册令牌哈希与 TTL（仅 pending 态可设）。
func SetEnrollToken(ctx context.Context, tx pgx.Tx, id, tokenHash string, ttl time.Duration) error {
	tag, err := tx.Exec(ctx, `UPDATE connectors SET enroll_token_hash = $2,
		enroll_token_expires_at = now() + $3, updated_at = now()
		WHERE id = $1 AND status = 'pending'`, id, tokenHash, ttl)
	if err != nil {
		return mapPgError(fmt.Errorf("set enroll token: %w", err))
	}
	if tag.RowsAffected() == 0 {
		return apierror.New(apierror.CodeConnectorAlreadyEnrolled)
	}
	return nil
}

// EnrollmentRecord 是注册校验所需的最小读取面（含敏感哈希，不出本包消费方）。
type EnrollmentRecord struct {
	Status    string
	TokenHash *string
	ExpiresAt *time.Time
}

// GetEnrollmentRecord 读注册校验字段。
func GetEnrollmentRecord(ctx context.Context, tx pgx.Tx, id string) (EnrollmentRecord, error) {
	var rec EnrollmentRecord
	err := tx.QueryRow(ctx, `SELECT status, enroll_token_hash, enroll_token_expires_at
		FROM connectors WHERE id = $1`, id).
		Scan(&rec.Status, &rec.TokenHash, &rec.ExpiresAt)
	if errors.Is(err, pgx.ErrNoRows) {
		return EnrollmentRecord{}, apierror.New(apierror.CodeCommonNotFound)
	}
	if err != nil {
		return EnrollmentRecord{}, fmt.Errorf("get enrollment record: %w", err)
	}
	return rec, nil
}

// CompleteEnrollment 注册成功收尾：置 enrolled、落指纹与版本、作废令牌哈希。
func CompleteEnrollment(ctx context.Context, tx pgx.Tx, id, fingerprint, version string) error {
	tag, err := tx.Exec(ctx, `UPDATE connectors SET status = 'enrolled',
		cert_fingerprint = $2, version = $3,
		enroll_token_hash = NULL, enroll_token_expires_at = NULL, updated_at = now()
		WHERE id = $1 AND status = 'pending'`, id, fingerprint, version)
	if err != nil {
		return mapPgError(fmt.Errorf("complete enrollment: %w", err))
	}
	if tag.RowsAffected() == 0 {
		return apierror.New(apierror.CodeConnectorAlreadyEnrolled)
	}
	return nil
}

// UpdateConnectorStatus 会话状态迁移（gateway 判定，console 记录；spec-1.2 §3）。
// revoked 为终态：任何会话态写入都不得覆盖。
func UpdateConnectorStatus(ctx context.Context, tx pgx.Tx, id, status string, heartbeatAt *time.Time) error {
	tag, err := tx.Exec(ctx, `UPDATE connectors SET status = $2,
		last_heartbeat_at = COALESCE($3, last_heartbeat_at), updated_at = now()
		WHERE id = $1 AND status NOT IN ('revoked', 'pending')`, id, status, heartbeatAt)
	if err != nil {
		return mapPgError(fmt.Errorf("update connector status: %w", err))
	}
	if tag.RowsAffected() == 0 {
		return connectorWriteRefusal(ctx, tx, id)
	}
	return nil
}

// RevokeConnector 吊销（幂等：已吊销返回成功）。
func RevokeConnector(ctx context.Context, tx pgx.Tx, id string) error {
	tag, err := tx.Exec(ctx, `UPDATE connectors SET status = 'revoked', revoked_at = now(),
		enroll_token_hash = NULL, enroll_token_expires_at = NULL, updated_at = now()
		WHERE id = $1 AND status <> 'revoked'`, id)
	if err != nil {
		return mapPgError(fmt.Errorf("revoke connector: %w", err))
	}
	if tag.RowsAffected() == 0 {
		// 不存在 → 404；已吊销 → 幂等成功
		if _, err := GetConnector(ctx, tx, id); err != nil {
			return err
		}
	}
	return nil
}

// connectorWriteRefusal 区分状态写入被拒的原因（吊销 vs 缺失/未注册）。
func connectorWriteRefusal(ctx context.Context, tx pgx.Tx, id string) error {
	c, err := GetConnector(ctx, tx, id)
	if err != nil {
		return err
	}
	if c.Status == "revoked" {
		return apierror.New(apierror.CodeConnectorRevoked)
	}
	return apierror.New(apierror.CodeCommonConflict).WithDetails(
		apierror.Detail{Field: "status", Reason: "接入器尚未完成注册（" + c.Status + "）"})
}

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
