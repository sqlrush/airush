package repo

import (
	"context"
	"fmt"

	"github.com/jackc/pgx/v5"
)

// 凭据只有写路径（创建/轮换）；读取/解密的唯一消费方是直连接入器（spec-1.17），
// 本 spec 有意不提供密文 getter——console API 面无任何凭据回显能力。

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
