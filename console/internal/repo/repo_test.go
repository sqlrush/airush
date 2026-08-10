package repo

import (
	"context"
	"errors"
	"testing"

	"github.com/jackc/pgx/v5"

	"github.com/sqlrush/airush/libs/apierror"
)

// TestInTenantTxMissingContext spec-1.1 T3（单测面）：无租户上下文时
// 在触达连接池之前即拒绝，返回 AR_TENANT_CONTEXT_MISSING。
func TestInTenantTxMissingContext(t *testing.T) {
	t.Parallel()

	s := &Store{pool: nil} // 若基座在拒绝前触池会 panic——测试同时覆盖"前置检查先行"
	err := s.InTenantTx(context.Background(), func(context.Context, pgx.Tx) error {
		t.Fatal("fn must not run without tenant context")
		return nil
	})

	var ae *apierror.Error
	if !errors.As(err, &ae) || ae.Code != apierror.CodeTenantContextMissing {
		t.Fatalf("err = %v, want AR_TENANT_CONTEXT_MISSING", err)
	}
}
