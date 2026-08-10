package repo

import (
	"errors"
	"strings"

	"github.com/jackc/pgx/v5/pgconn"

	"github.com/sqlrush/airush/libs/apierror"
)

// mapPgError 把约束违规映射为注册错误码（spec-1.1 §2.3）；
// 未识别的错误原样返回（上层归一 AR_INTERNAL_ERROR）。
func mapPgError(err error) error {
	var pgErr *pgconn.PgError
	if !errors.As(err, &pgErr) {
		return err
	}
	switch pgErr.Code {
	case "23505": // unique_violation
		return mapUniqueViolation(pgErr, err)
	case "23514": // check_violation
		if strings.HasPrefix(pgErr.ConstraintName, "mode_") {
			return apierror.Wrap(apierror.CodeDatasourceModeMismatch, err)
		}
		return apierror.Wrap(apierror.CodeValidationFailed, err).WithDetails(
			apierror.Detail{Field: pgErr.ConstraintName, Reason: "字段组合不满足约束"})
	case "23503": // foreign_key_violation：引用对象不存在（或跨租户，复合 FK 同样在此拦截）
		return apierror.Wrap(apierror.CodeValidationFailed, err).WithDetails(
			apierror.Detail{Field: pgErr.ConstraintName, Reason: "引用的对象不存在"})
	}
	return err
}

func mapUniqueViolation(pgErr *pgconn.PgError, err error) error {
	switch {
	case strings.Contains(pgErr.ConstraintName, "datasources_tenant_id_name"):
		return apierror.Wrap(apierror.CodeDatasourceNameConflict, err)
	case strings.Contains(pgErr.ConstraintName, "datasource_aliases_tenant_id_alias"):
		return apierror.Wrap(apierror.CodeAliasConflict, err)
	default:
		return apierror.Wrap(apierror.CodeCommonConflict, err)
	}
}
