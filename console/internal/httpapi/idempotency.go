package httpapi

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"net/http"

	"github.com/sqlrush/airush/console/internal/repo"
	"github.com/sqlrush/airush/libs/apierror"
)

// createWithIdempotency 统一创建路径：带 Idempotency-Key 时查/存响应快照
// （与业务写同事务）；同 key 同 payload 重放原响应，payload 不同 → 409（spec-1.1 T9）。
func (s *Server) createWithIdempotency(w http.ResponseWriter, r *http.Request, body []byte,
	create func(ctx context.Context, tx repo.Tx) (any, error),
) error {
	key := r.Header.Get("Idempotency-Key")
	if key == "" {
		return s.createPlain(w, r, create)
	}

	hash := sha256Hex(body)
	var respStatus int
	var respBody []byte
	err := s.store.InTenantTx(r.Context(), func(ctx context.Context, tx repo.Tx) error {
		rec, err := repo.GetIdempotencyRecord(ctx, tx, key)
		if err != nil {
			return err
		}
		if rec != nil {
			if rec.RequestHash != hash {
				return apierror.New(apierror.CodeIdempotencyReplay)
			}
			respStatus, respBody = rec.ResponseStatus, rec.ResponseBody
			return nil
		}
		v, err := create(ctx, tx)
		if err != nil {
			return err
		}
		buf, err := json.Marshal(v)
		if err != nil {
			return fmt.Errorf("marshal idempotent response: %w", err)
		}
		if err := repo.PutIdempotencyRecord(ctx, tx, key, hash, http.StatusCreated, buf); err != nil {
			return err
		}
		respStatus, respBody = http.StatusCreated, buf
		return nil
	})
	if err != nil {
		return err
	}

	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.WriteHeader(respStatus)
	if _, err := w.Write(respBody); err != nil {
		return fmt.Errorf("write idempotent response: %w", err)
	}
	return nil
}

// createPlain 无幂等键的直接创建路径。
func (s *Server) createPlain(w http.ResponseWriter, r *http.Request,
	create func(ctx context.Context, tx repo.Tx) (any, error),
) error {
	var result any
	err := s.store.InTenantTx(r.Context(), func(ctx context.Context, tx repo.Tx) error {
		v, err := create(ctx, tx)
		result = v
		return err
	})
	if err != nil {
		return err
	}
	return writeJSON(w, http.StatusCreated, result)
}

func sha256Hex(b []byte) string {
	sum := sha256.Sum256(b)
	return hex.EncodeToString(sum[:])
}
