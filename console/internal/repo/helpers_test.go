package repo

import (
	"errors"
	"testing"

	"github.com/jackc/pgx/v5/pgconn"

	"github.com/sqlrush/airush/libs/apierror"
)

func TestMapPgError(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name       string
		pgCode     string
		constraint string
		wantCode   apierror.Code
	}{
		{"数据源重名", "23505", "datasources_tenant_id_name_key", apierror.CodeDatasourceNameConflict},
		{"别名冲突", "23505", "datasource_aliases_tenant_id_alias_key", apierror.CodeAliasConflict},
		{"其他唯一冲突", "23505", "users_pkey", apierror.CodeCommonConflict},
		{"模式 CHECK", "23514", "mode_direct_shape", apierror.CodeDatasourceModeMismatch},
		{"组配对 CHECK", "23514", "group_role_pairing", apierror.CodeValidationFailed},
		{"外键违规", "23503", "datasources_tenant_id_connector_id_fkey", apierror.CodeValidationFailed},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			err := mapPgError(&pgconn.PgError{Code: tt.pgCode, ConstraintName: tt.constraint})
			var ae *apierror.Error
			if !errors.As(err, &ae) || ae.Code != tt.wantCode {
				t.Fatalf("mapped = %v, want %s", err, tt.wantCode)
			}
		})
	}

	plain := errors.New("not a pg error")
	if got := mapPgError(plain); !errors.Is(got, plain) {
		t.Fatalf("non-pg error rewritten: %v", got)
	}
}

func TestBuildDatasourceSets(t *testing.T) {
	t.Parallel()

	name := "n"
	port := 5433
	empty := ""
	gid := "55555555-5555-5555-5555-555555555555"

	sets, args := buildDatasourceSets(DatasourcePatch{})
	if len(sets) != 0 || len(args) != 0 {
		t.Fatalf("empty patch produced sets %v", sets)
	}

	sets, args = buildDatasourceSets(DatasourcePatch{Name: &name, Port: &port})
	if len(sets) != 2 || len(args) != 2 {
		t.Fatalf("sets = %v args = %v", sets, args)
	}
	if sets[0] != "name = $1" || sets[1] != "port = $2" {
		t.Fatalf("sets = %v", sets)
	}

	// 解绑：空串 → NULL 参数
	sets, args = buildDatasourceSets(DatasourcePatch{GroupID: &empty, GroupRole: &empty})
	if len(sets) != 2 {
		t.Fatalf("sets = %v", sets)
	}
	if args[0] != (*string)(nil) || args[1] != (*string)(nil) {
		t.Fatalf("unbind args = %#v, want typed nils", args)
	}

	// 绑定：非空原样
	sets, args = buildDatasourceSets(DatasourcePatch{GroupID: &gid})
	if v, ok := args[0].(*string); !ok || v == nil || *v != gid {
		t.Fatalf("bind arg = %#v", args[0])
	}
	_ = sets
}

func TestCursorArgs(t *testing.T) {
	t.Parallel()

	args := cursorArgs(nil, 10)
	if len(args) != 3 || args[0] != nil || args[2] != 10 {
		t.Fatalf("nil cursor args = %#v", args)
	}
	cur := &PageCursor{ID: "id-1"}
	args = cursorArgs(cur, 5)
	if args[0] != cur.CreatedAt || args[1] != "id-1" || args[2] != 5 {
		t.Fatalf("cursor args = %#v", args)
	}
}

func TestInUseDetails(t *testing.T) {
	t.Parallel()

	g, a := "g", "a"
	if d := inUseDetails(Datasource{GroupID: &g, AgentID: &a}); len(d) != 2 {
		t.Fatalf("details = %+v", d)
	}
	if d := inUseDetails(Datasource{}); len(d) != 0 {
		t.Fatalf("details = %+v", d)
	}
}
