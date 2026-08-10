//go:build integration

package httpapi

import (
	"bytes"
	"context"
	"crypto/rand"
	"database/sql"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	_ "github.com/jackc/pgx/v5/stdlib"

	"github.com/sqlrush/airush/console/internal/credcrypto"
	"github.com/sqlrush/airush/console/internal/dbmigrate"
	"github.com/sqlrush/airush/console/internal/repo"
	"github.com/sqlrush/airush/testkit"
)

const (
	devTenantID = "00000000-0000-0000-0000-000000000001"
	tenantBID   = "22222222-2222-2222-2222-222222222222"
	connectorID = "55555555-5555-5555-5555-555555555555"

	secretPassword = "s3cret-dz-password" //nolint:gosec // 测试注入的假凭据，用于泄漏断言
	rotatedSecret  = "r0tated-dz-password"
)

// apiEnv 是集成测试环境：dev 与 B 两个租户视角的 server 共享同一 store。
type apiEnv struct {
	dev        *httptest.Server
	b          *httptest.Server
	admin      *sql.DB
	sealer     *credcrypto.Sealer
	transcript *bytes.Buffer // 全部响应字节，末尾做明文泄漏扫描（T5）
}

func newAPIEnv(t *testing.T) *apiEnv {
	t.Helper()
	ctx := context.Background()

	pg, err := testkit.StartPostgres(ctx)
	if err != nil {
		t.Fatalf("start postgres: %v", err)
	}
	t.Cleanup(func() { _ = pg.Terminate(context.Background()) })

	if err := dbmigrate.RunWithURL(pg.ConnString, []string{"up"}); err != nil {
		t.Fatalf("migrate up: %v", err)
	}

	admin, err := sql.Open("pgx", pg.ConnString)
	if err != nil {
		t.Fatalf("open admin: %v", err)
	}
	t.Cleanup(func() { _ = admin.Close() })
	mustExecSQL(t, admin, `INSERT INTO tenants (id, name, slug) VALUES ($1, '租户B', 'tenant-b')`, tenantBID)
	mustExecSQL(t, admin, `INSERT INTO connectors (tenant_id, id, name) VALUES ($1, $2, 'conn-dev')`,
		devTenantID, connectorID)

	store, err := repo.New(ctx, pg.ConnString)
	if err != nil {
		t.Fatalf("new store: %v", err)
	}
	t.Cleanup(store.Close)

	kek := make([]byte, 32)
	if _, err := rand.Read(kek); err != nil {
		t.Fatalf("rand: %v", err)
	}
	sealer, err := credcrypto.New(base64.StdEncoding.EncodeToString(kek), "v1")
	if err != nil {
		t.Fatalf("sealer: %v", err)
	}

	env := &apiEnv{admin: admin, sealer: sealer, transcript: &bytes.Buffer{}}
	for _, cfg := range []struct {
		tenant string
		dst    **httptest.Server
	}{{devTenantID, &env.dev}, {tenantBID, &env.b}} {
		api, err := New(store, sealer, cfg.tenant)
		if err != nil {
			t.Fatalf("new server: %v", err)
		}
		srv := httptest.NewServer(api.Handler())
		t.Cleanup(srv.Close)
		*cfg.dst = srv
	}
	return env
}

// do 发请求并把响应记入 transcript（T5 泄漏扫描材料）。
func (e *apiEnv) do(t *testing.T, srv *httptest.Server, method, path string, body any, headers map[string]string) (int, []byte) {
	t.Helper()
	var reader io.Reader
	if body != nil {
		buf, err := json.Marshal(body)
		if err != nil {
			t.Fatalf("marshal request: %v", err)
		}
		reader = bytes.NewReader(buf)
	}
	req, err := http.NewRequest(method, srv.URL+path, reader)
	if err != nil {
		t.Fatalf("new request: %v", err)
	}
	for k, v := range headers {
		req.Header.Set(k, v)
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("%s %s: %v", method, path, err)
	}
	defer func() { _ = resp.Body.Close() }()
	respBody, err := io.ReadAll(resp.Body)
	if err != nil {
		t.Fatalf("read response: %v", err)
	}
	e.transcript.Write(respBody)
	return resp.StatusCode, respBody
}

func jsonMap(t *testing.T, b []byte) map[string]any {
	t.Helper()
	var m map[string]any
	if err := json.Unmarshal(b, &m); err != nil {
		t.Fatalf("unmarshal %.80s: %v", b, err)
	}
	return m
}

func wantCode(t *testing.T, status int, body []byte, wantStatus int, wantCode string) {
	t.Helper()
	if status != wantStatus {
		t.Fatalf("status = %d body=%.200s, want %d", status, body, wantStatus)
	}
	if got := jsonMap(t, body)["code"]; got != wantCode {
		t.Fatalf("code = %v, want %s", got, wantCode)
	}
}

func mustExecSQL(t *testing.T, db *sql.DB, q string, args ...any) {
	t.Helper()
	if _, err := db.Exec(q, args...); err != nil {
		t.Fatalf("exec %.60s: %v", q, err)
	}
}

// TestAPIIntegration spec-1.1 D6 主电池：T2/T4/T5/T6/T7/T8/T9/T10 顺序执行于同一环境。
func TestAPIIntegration(t *testing.T) {
	env := newAPIEnv(t)

	var directDSID string

	t.Run("T6 模式形态校验", func(t *testing.T) {
		status, body := env.do(t, env.dev, "POST", "/api/v1/datasources", map[string]any{
			"name": "bad", "engine_family": "postgres", "connect_mode": "direct",
			"connector_id": connectorID, "host": "h", "port": 5432,
		}, nil)
		wantCode(t, status, body, 400, "AR_DATASOURCE_MODE_MISMATCH")

		status, body = env.do(t, env.dev, "POST", "/api/v1/datasources", map[string]any{
			"name": "bad2", "engine_family": "postgres", "connect_mode": "connector",
			"host": "h", "port": 5432,
		}, nil)
		wantCode(t, status, body, 400, "AR_DATASOURCE_MODE_MISMATCH")
	})

	t.Run("创建两种模式数据源", func(t *testing.T) {
		status, body := env.do(t, env.dev, "POST", "/api/v1/datasources", map[string]any{
			"name": "og-conn", "engine_family": "postgres", "engine": "opengauss",
			"connect_mode": "connector", "connector_id": connectorID, "host": "10.0.0.1", "port": 5432,
		}, nil)
		if status != 201 {
			t.Fatalf("create connector-mode: %d %.200s", status, body)
		}

		status, body = env.do(t, env.dev, "POST", "/api/v1/datasources", map[string]any{
			"name": "og-direct", "engine_family": "postgres", "engine": "opengauss",
			"connect_mode": "direct", "host": "10.0.0.2", "port": 5432,
			"credential": map[string]string{"username": "dba", "password": secretPassword},
		}, nil)
		if status != 201 {
			t.Fatalf("create direct-mode: %d %.200s", status, body)
		}
		m := jsonMap(t, body)
		directDSID, _ = m["id"].(string)
		if directDSID == "" {
			t.Fatalf("no id in response: %.200s", body)
		}
		if m["has_credential"] != true {
			t.Fatalf("has_credential = %v, want true", m["has_credential"])
		}

		// 重名 → 409（T7：AR_DATASOURCE_NAME_CONFLICT）
		status, body = env.do(t, env.dev, "POST", "/api/v1/datasources", map[string]any{
			"name": "og-conn", "engine_family": "postgres", "connect_mode": "connector",
			"connector_id": connectorID, "host": "h", "port": 5432,
		}, nil)
		wantCode(t, status, body, 409, "AR_DATASOURCE_NAME_CONFLICT")
	})

	t.Run("T4 凭据密文落库与 roundtrip", func(t *testing.T) {
		var ciphertext []byte
		err := env.admin.QueryRow(`SELECT c.secret_ciphertext
			FROM datasource_credentials c JOIN datasources d ON d.credential_id = c.id
			WHERE d.id = $1`, directDSID).Scan(&ciphertext)
		if err != nil {
			t.Fatalf("read ciphertext: %v", err)
		}
		if bytes.Contains(ciphertext, []byte(secretPassword)) {
			t.Fatal("ciphertext contains plaintext password")
		}
		plain, err := env.sealer.Open(ciphertext)
		if err != nil {
			t.Fatalf("open: %v", err)
		}
		if string(plain) != secretPassword {
			t.Fatalf("roundtrip = %q", plain)
		}
	})

	t.Run("凭据轮换与模式护栏", func(t *testing.T) {
		status, body := env.do(t, env.dev, "PUT", "/api/v1/datasources/"+directDSID+"/credential",
			map[string]string{"username": "dba2", "password": rotatedSecret}, nil)
		if status != 204 {
			t.Fatalf("rotate: %d %.200s", status, body)
		}
		var rotated sql.NullString
		if err := env.admin.QueryRow(`SELECT c.rotated_at::text
			FROM datasource_credentials c JOIN datasources d ON d.credential_id = c.id
			WHERE d.id = $1`, directDSID).Scan(&rotated); err != nil {
			t.Fatalf("read rotated_at: %v", err)
		}
		if !rotated.Valid {
			t.Fatal("rotated_at not set after rotation")
		}

		// connector 模式数据源设凭据 → 400 MODE_MISMATCH
		var connDSID string
		if err := env.admin.QueryRow(`SELECT id FROM datasources WHERE name = 'og-conn'`).
			Scan(&connDSID); err != nil {
			t.Fatalf("find og-conn: %v", err)
		}
		status, body = env.do(t, env.dev, "PUT", "/api/v1/datasources/"+connDSID+"/credential",
			map[string]string{"username": "x", "password": "y"}, nil)
		wantCode(t, status, body, 400, "AR_DATASOURCE_MODE_MISMATCH")
	})

	t.Run("T2 API 层租户隔离", func(t *testing.T) {
		status, body := env.do(t, env.dev, "GET", "/api/v1/datasources", nil, nil)
		if status != 200 || len(jsonMap(t, body)["items"].([]any)) != 2 {
			t.Fatalf("dev list: %d %.200s, want 2 items", status, body)
		}
		status, body = env.do(t, env.b, "GET", "/api/v1/datasources", nil, nil)
		if status != 200 || len(jsonMap(t, body)["items"].([]any)) != 0 {
			t.Fatalf("tenant B list: %d %.200s, want 0 items (RLS breach!)", status, body)
		}
		// 租户 B 按 id 直取 dev 的数据源 → 404（不可见即不存在）
		status, body = env.do(t, env.b, "GET", "/api/v1/datasources/"+directDSID, nil, nil)
		wantCode(t, status, body, 404, "AR_DATASOURCE_NOT_FOUND")
	})

	t.Run("T7 查无与非法游标", func(t *testing.T) {
		status, body := env.do(t, env.dev, "GET",
			"/api/v1/datasources/99999999-9999-9999-9999-999999999999", nil, nil)
		wantCode(t, status, body, 404, "AR_DATASOURCE_NOT_FOUND")
		status, body = env.do(t, env.dev, "GET",
			"/api/v1/agents/99999999-9999-9999-9999-999999999999", nil, nil)
		wantCode(t, status, body, 404, "AR_AGENT_NOT_FOUND")
	})

	t.Run("T5 响应全程无凭据明文", func(t *testing.T) {
		for _, secret := range []string{secretPassword, rotatedSecret} {
			if strings.Contains(env.transcript.String(), secret) {
				t.Fatalf("API 响应流含凭据明文 %q", secret)
			}
		}
	})

	runPaginationAndIdempotency(t, env)
	runBindingsAndAliases(t, env, &directDSID)

	t.Run("T5 终扫", func(t *testing.T) {
		for _, secret := range []string{secretPassword, rotatedSecret} {
			if strings.Contains(env.transcript.String(), secret) {
				t.Fatalf("API 响应流含凭据明文 %q", secret)
			}
		}
	})
}

// runPaginationAndIdempotency T8/T9。
func runPaginationAndIdempotency(t *testing.T, env *apiEnv) {
	t.Run("T8 keyset 分页", func(t *testing.T) {
		for i := 0; i < 25; i++ {
			status, body := env.do(t, env.dev, "POST", "/api/v1/agents", map[string]any{
				"name": fmt.Sprintf("agent-%02d", i), "kind": "domain",
			}, nil)
			if status != 201 {
				t.Fatalf("create agent %d: %d %.200s", i, status, body)
			}
		}
		seen := map[string]bool{}
		cursor := ""
		pages := 0
		for {
			path := "/api/v1/agents?limit=10"
			if cursor != "" {
				path += "&cursor=" + cursor
			}
			status, body := env.do(t, env.dev, "GET", path, nil, nil)
			if status != 200 {
				t.Fatalf("list agents: %d %.200s", status, body)
			}
			m := jsonMap(t, body)
			for _, it := range m["items"].([]any) {
				id := it.(map[string]any)["id"].(string)
				if seen[id] {
					t.Fatalf("duplicate id %s across pages", id)
				}
				seen[id] = true
			}
			pages++
			nc, _ := m["next_cursor"].(string)
			if nc == "" {
				break
			}
			cursor = nc
			if pages > 5 {
				t.Fatal("pagination did not terminate")
			}
		}
		if len(seen) != 25 {
			t.Fatalf("paged total = %d, want 25", len(seen))
		}

		status, body := env.do(t, env.dev, "GET", "/api/v1/agents?cursor=not-a-cursor", nil, nil)
		wantCode(t, status, body, 400, "AR_VALIDATION_FAILED")
	})

	t.Run("T9 幂等键", func(t *testing.T) {
		hdr := map[string]string{"Idempotency-Key": "idem-key-1"}
		req := map[string]any{"name": "idem-agent", "kind": "assistant"}

		status, body := env.do(t, env.dev, "POST", "/api/v1/agents", req, hdr)
		if status != 201 {
			t.Fatalf("first: %d %.200s", status, body)
		}
		firstID := jsonMap(t, body)["id"]

		status, body = env.do(t, env.dev, "POST", "/api/v1/agents", req, hdr)
		if status != 201 || jsonMap(t, body)["id"] != firstID {
			t.Fatalf("replay: %d %.200s, want同 id %v", status, body, firstID)
		}

		status, body = env.do(t, env.dev, "POST", "/api/v1/agents",
			map[string]any{"name": "different", "kind": "assistant"}, hdr)
		wantCode(t, status, body, 409, "AR_IDEMPOTENCY_REPLAY")
	})
}

// runBindingsAndAliases T10 + 别名冲突 + 引用保护。
func runBindingsAndAliases(t *testing.T, env *apiEnv, directDSID *string) {
	var groupID string

	t.Run("T10 引用保护", func(t *testing.T) {
		status, body := env.do(t, env.dev, "POST", "/api/v1/datasource-groups",
			map[string]any{"name": "g-main", "kind": "primary_standby"}, nil)
		if status != 201 {
			t.Fatalf("create group: %d %.200s", status, body)
		}
		groupID = jsonMap(t, body)["id"].(string)

		status, body = env.do(t, env.dev, "PATCH", "/api/v1/datasources/"+*directDSID,
			map[string]any{"group_id": groupID, "group_role": "primary"}, nil)
		if status != 200 {
			t.Fatalf("bind group: %d %.200s", status, body)
		}

		status, body = env.do(t, env.dev, "DELETE", "/api/v1/datasources/"+*directDSID, nil, nil)
		wantCode(t, status, body, 409, "AR_DATASOURCE_IN_USE")

		status, body = env.do(t, env.dev, "DELETE", "/api/v1/datasource-groups/"+groupID, nil, nil)
		wantCode(t, status, body, 409, "AR_COMMON_CONFLICT")

		status, body = env.do(t, env.dev, "PATCH", "/api/v1/datasources/"+*directDSID,
			map[string]any{"group_id": "", "group_role": ""}, nil)
		if status != 200 {
			t.Fatalf("unbind group: %d %.200s", status, body)
		}
	})

	t.Run("别名生命周期与冲突", func(t *testing.T) {
		status, body := env.do(t, env.dev, "POST", "/api/v1/datasources/"+*directDSID+"/aliases",
			map[string]any{"alias": "生产库"}, nil)
		if status != 201 {
			t.Fatalf("create alias: %d %.200s", status, body)
		}
		status, body = env.do(t, env.dev, "POST", "/api/v1/datasources/"+*directDSID+"/aliases",
			map[string]any{"alias": "生产库", "source": "conversation"}, nil)
		wantCode(t, status, body, 409, "AR_ALIAS_CONFLICT")

		status, body = env.do(t, env.dev, "GET", "/api/v1/datasources/"+*directDSID+"/aliases", nil, nil)
		if status != 200 || len(jsonMap(t, body)["items"].([]any)) != 1 {
			t.Fatalf("list aliases: %d %.200s", status, body)
		}
		status, body = env.do(t, env.dev, "DELETE",
			"/api/v1/datasources/"+*directDSID+"/aliases/生产库", nil, nil)
		if status != 204 {
			t.Fatalf("delete alias: %d %.200s", status, body)
		}
	})

	t.Run("数据源删除与凭据清理", func(t *testing.T) {
		status, body := env.do(t, env.dev, "DELETE", "/api/v1/datasources/"+*directDSID, nil, nil)
		if status != 204 {
			t.Fatalf("delete: %d %.200s", status, body)
		}
		var n int
		if err := env.admin.QueryRow(
			`SELECT count(*) FROM datasource_credentials`).Scan(&n); err != nil {
			t.Fatalf("count credentials: %v", err)
		}
		if n != 0 {
			t.Fatalf("orphan credentials = %d, want 0", n)
		}
		status, body = env.do(t, env.dev, "DELETE", "/api/v1/datasource-groups/"+groupID, nil, nil)
		if status != 204 {
			t.Fatalf("delete group: %d %.200s", status, body)
		}
	})
}
