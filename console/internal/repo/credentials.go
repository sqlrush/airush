package repo

import (
	"context"
	"errors"
	"fmt"

	"github.com/jackc/pgx/v5"

	"github.com/sqlrush/airush/libs/apierror"
)

// 凭据写路径（创建/轮换）见下；读取/解密的唯一消费方是直连接入器（spec-1.17，
// GetDirectConnInfo）——console API 面无任何凭据回显能力。

// DirectConnInfo 是直连建连所需的全部信息（含凭据密文）。唯一消费方是
// internal/directconn（spec-1.17，AD-4 直连模式的唯一解密方）；密文在此出库、
// 在 directconn 单函数栈帧内解密，绝不进日志/响应。
type DirectConnInfo struct {
	Host         string
	Port         int
	DatabaseName string
	EngineFamily string
	ConnectMode  string
	Username     string
	Ciphertext   []byte
	KeyID        string
}

// GetDirectConnInfo 读直连信息（datasources ⨝ datasource_credentials）。
// 非 direct 模式或无凭据时返回 AR_DATASOURCE_MODE_MISMATCH / NOT_FOUND。
func GetDirectConnInfo(ctx context.Context, tx pgx.Tx, datasourceID string) (DirectConnInfo, error) {
	var info DirectConnInfo
	err := tx.QueryRow(ctx, `SELECT d.host, d.port, d.database_name, d.engine_family,
		d.connect_mode, c.username, c.secret_ciphertext, c.key_id
		FROM datasources d
		JOIN datasource_credentials c ON c.tenant_id = d.tenant_id AND c.id = d.credential_id
		WHERE d.id = $1`, datasourceID).
		Scan(&info.Host, &info.Port, &info.DatabaseName, &info.EngineFamily,
			&info.ConnectMode, &info.Username, &info.Ciphertext, &info.KeyID)
	if errors.Is(err, pgx.ErrNoRows) {
		// datasource 不存在，或非 direct 模式（无 credential_id → JOIN 落空）
		return DirectConnInfo{}, directConnUnavailable(ctx, tx, datasourceID)
	}
	if err != nil {
		return DirectConnInfo{}, fmt.Errorf("get direct conn info: %w", err)
	}
	return info, nil
}

// directConnUnavailable 区分"数据源不存在"与"非直连模式/无凭据"。
func directConnUnavailable(ctx context.Context, tx pgx.Tx, datasourceID string) error {
	ds, err := GetDatasource(ctx, tx, datasourceID)
	if err != nil {
		return err // AR_DATASOURCE_NOT_FOUND
	}
	if ds.ConnectMode != "direct" {
		return apierror.New(apierror.CodeDatasourceModeMismatch).WithDetails(
			apierror.Detail{Field: "connect_mode", Reason: "仅直连模式数据源支持连接测试"})
	}
	return apierror.New(apierror.CodeValidationFailed).WithDetails(
		apierror.Detail{Field: "credential", Reason: "直连数据源尚未设置凭据"})
}

// InsertCredential 落库信封密文，返回凭据 id。
func InsertCredential(ctx context.Context, tx pgx.Tx, username string, ciphertext []byte, keyID string) (string, error) {
	var id string
	err := tx.QueryRow(ctx, `INSERT INTO datasource_credentials
		(tenant_id, username, secret_ciphertext, key_id)
		VALUES (`+tenantExpr+`, $1, $2, $3) RETURNING id`,
		username, ciphertext, keyID).Scan(&id)
	if err != nil {
		return "", mapPgError(fmt.Errorf("insert credential: %w", err))
	}
	return id, nil
}

// RotateDatasourceCredential 轮换数据源当前凭据（按 datasource id 定位，
// 凭据 id 不变，datasources.credential_id 无需联动）。
func RotateDatasourceCredential(ctx context.Context, tx pgx.Tx, datasourceID, username string, ciphertext []byte, keyID string) error {
	var credentialID string
	err := tx.QueryRow(ctx, `SELECT credential_id FROM datasources
		WHERE id = $1 AND credential_id IS NOT NULL`, datasourceID).Scan(&credentialID)
	if err != nil {
		return fmt.Errorf("locate datasource credential: %w", err)
	}
	return RotateCredential(ctx, tx, credentialID, username, ciphertext, keyID)
}

// RotateCredential 原地轮换（保持凭据 id 不变，datasources.credential_id 无需联动）。
func RotateCredential(ctx context.Context, tx pgx.Tx, id, username string, ciphertext []byte, keyID string) error {
	tag, err := tx.Exec(ctx, `UPDATE datasource_credentials
		SET username = $2, secret_ciphertext = $3, key_id = $4, rotated_at = now()
		WHERE id = $1`, id, username, ciphertext, keyID)
	if err != nil {
		return mapPgError(fmt.Errorf("rotate credential: %w", err))
	}
	if tag.RowsAffected() == 0 {
		return fmt.Errorf("rotate credential: credential %s not visible in tenant tx", id)
	}
	return nil
}
