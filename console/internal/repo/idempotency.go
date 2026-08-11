package repo

import (
	"context"
	"errors"
	"fmt"

	"github.com/jackc/pgx/v5"
)

// IdempotencyRecord 是已存储的幂等响应快照。
type IdempotencyRecord struct {
	RequestHash    string
	ResponseStatus int
	ResponseBody   []byte
}

// GetIdempotencyRecord 查幂等键；不存在返回 (nil, nil)。
func GetIdempotencyRecord(ctx context.Context, tx pgx.Tx, key string) (*IdempotencyRecord, error) {
	var rec IdempotencyRecord
	err := tx.QueryRow(ctx, `SELECT request_hash, response_status, response_body
		FROM idempotency_keys WHERE key = $1`, key).
		Scan(&rec.RequestHash, &rec.ResponseStatus, &rec.ResponseBody)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("get idempotency record: %w", err)
	}
	return &rec, nil
}

// PutIdempotencyRecord 存储响应快照（与业务写同事务，保证"创建成功必有快照"）。
func PutIdempotencyRecord(ctx context.Context, tx pgx.Tx, key, requestHash string, status int, body []byte) error {
	if _, err := tx.Exec(ctx, `INSERT INTO idempotency_keys
		(tenant_id, key, request_hash, response_status, response_body)
		VALUES (`+tenantExpr+`, $1, $2, $3, $4)`,
		key, requestHash, status, body); err != nil {
		return mapPgError(fmt.Errorf("put idempotency record: %w", err))
	}
	return nil
}
