//go:build integration

// spec-1.2 D6 console 侧：svcapi 内部 API 对真实 PG+repo+pki 的注册/握手/状态电池。
// 覆盖 T2（token 过期/复用/跨租户）、T5（指纹绑定）、T11（svc token）与 enroll 签发。
package svcapi_test

import (
	"context"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/x509"
	"crypto/x509/pkix"
	"database/sql"
	"encoding/json"
	"encoding/pem"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	_ "github.com/jackc/pgx/v5/stdlib"

	"github.com/sqlrush/airush/console/internal/dbmigrate"
	"github.com/sqlrush/airush/console/internal/enrolltoken"
	"github.com/sqlrush/airush/console/internal/pki"
	"github.com/sqlrush/airush/console/internal/repo"
	"github.com/sqlrush/airush/console/internal/svcapi"
	"github.com/sqlrush/airush/console/internal/tenancy"
	"github.com/sqlrush/airush/libs/metrics"
	"github.com/sqlrush/airush/testkit"
)

const (
	devTenantID = "00000000-0000-0000-0000-000000000001"
	svcToken    = "svc-token-xyz"
)

type env struct {
	admin *sql.DB
	store *repo.Store
	srv   *httptest.Server
}

func newEnv(t *testing.T) *env {
	t.Helper()
	ctx := context.Background()
	pg, err := testkit.StartPostgres(ctx)
	if err != nil {
		t.Fatalf("postgres: %v", err)
	}
	t.Cleanup(func() { _ = pg.Terminate(context.Background()) })
	if err := dbmigrate.RunWithURL(pg.ConnString, []string{"up"}); err != nil {
		t.Fatalf("migrate: %v", err)
	}
	admin, err := sql.Open("pgx", pg.ConnString)
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	t.Cleanup(func() { _ = admin.Close() })

	store, err := repo.New(ctx, pg.ConnString)
	if err != nil {
		t.Fatalf("store: %v", err)
	}
	t.Cleanup(store.Close)

	caCertPEM, caKeyPEM, _ := pki.Generate("ca")
	ca, _ := pki.Load(caCertPEM, caKeyPEM)
	srv := httptest.NewServer(svcapi.New(store, ca, svcToken).Handler())
	t.Cleanup(srv.Close)

	return &env{admin: admin, store: store, srv: srv}
}

// createConnector 直接经 repo 建实体 + token（绕过公开 API，聚焦 svcapi）。
func (e *env) createConnector(t *testing.T, ttl time.Duration) (id, token string) {
	t.Helper()
	ctx := tenancy.WithTenant(context.Background(), devTenantID)
	err := e.store.InTenantTx(ctx, func(ctx context.Context, tx repo.Tx) error {
		c, err := repo.InsertConnector(ctx, tx, repo.ConnectorInput{Name: "c-" + randSuffix(t)})
		if err != nil {
			return err
		}
		id = c.ID
		tk, hash, err := enrolltoken.New(devTenantID, c.ID)
		if err != nil {
			return err
		}
		token = tk
		return repo.SetEnrollToken(ctx, tx, c.ID, hash, ttl)
	})
	if err != nil {
		t.Fatalf("create connector: %v", err)
	}
	return id, token
}

func (e *env) post(t *testing.T, path, token string, body any) (int, map[string]any) {
	t.Helper()
	buf, _ := json.Marshal(body)
	req, _ := http.NewRequest("POST", e.srv.URL+path, strings.NewReader(string(buf)))
	req.Header.Set("Content-Type", "application/json")
	if token != "" {
		req.Header.Set("Authorization", "Bearer "+token)
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("post %s: %v", path, err)
	}
	defer func() { _ = resp.Body.Close() }()
	var m map[string]any
	_ = json.NewDecoder(resp.Body).Decode(&m)
	return resp.StatusCode, m
}

func TestEnrollHappyPath(t *testing.T) {
	e := newEnv(t)
	id, token := e.createConnector(t, 15*time.Minute)

	status, resp := e.post(t, "/internal/v1/connector-enrollments", svcToken, map[string]string{
		"token": token, "csr_pem": makeCSR(t), "connector_version": "v1",
	})
	if status != 200 {
		t.Fatalf("enroll status %d: %v", status, resp)
	}
	if resp["connector_id"] != id || resp["certificate_pem"] == "" {
		t.Fatalf("enroll resp = %v", resp)
	}
	// 库态：enrolled + 指纹落 + token 作废
	var dbStatus string
	var fp string
	var tokenHash *string
	if err := e.admin.QueryRow(`SELECT status, cert_fingerprint, enroll_token_hash
		FROM connectors WHERE id = $1`, id).Scan(&dbStatus, &fp, &tokenHash); err != nil {
		t.Fatalf("read: %v", err)
	}
	if dbStatus != "enrolled" || fp == "" || tokenHash != nil {
		t.Fatalf("post-enroll state: status=%s fp=%q hash=%v", dbStatus, fp, tokenHash)
	}
}

func TestEnrollTokenReuseAndExpiry(t *testing.T) {
	e := newEnv(t)

	// 复用：首次成功后二次拒绝
	_, token := e.createConnector(t, 15*time.Minute)
	if s, _ := e.post(t, "/internal/v1/connector-enrollments", svcToken, map[string]string{
		"token": token, "csr_pem": makeCSR(t),
	}); s != 200 {
		t.Fatalf("first enroll %d", s)
	}
	s, resp := e.post(t, "/internal/v1/connector-enrollments", svcToken, map[string]string{
		"token": token, "csr_pem": makeCSR(t),
	})
	if s == 200 {
		t.Fatal("token reuse accepted")
	}
	_ = resp

	// 过期：TTL 负值 → 立即过期
	_, expToken := e.createConnector(t, -time.Minute)
	s, resp = e.post(t, "/internal/v1/connector-enrollments", svcToken, map[string]string{
		"token": expToken, "csr_pem": makeCSR(t),
	})
	if s != 401 || resp["code"] != "AR_CONNECTOR_ENROLL_TOKEN_INVALID" {
		t.Fatalf("expired token: %d %v", s, resp)
	}
}

func TestEnrollTamperedTokenRejected(t *testing.T) {
	e := newEnv(t)
	_, token := e.createConnector(t, 15*time.Minute)
	// 篡改 secret 部分
	tampered := token[:len(token)-4] + "0000"
	s, resp := e.post(t, "/internal/v1/connector-enrollments", svcToken, map[string]string{
		"token": tampered, "csr_pem": makeCSR(t),
	})
	if s != 401 || resp["code"] != "AR_CONNECTOR_ENROLL_TOKEN_INVALID" {
		t.Fatalf("tampered token: %d %v", s, resp)
	}
}

func TestSvcTokenRequired(t *testing.T) {
	e := newEnv(t)
	_, token := e.createConnector(t, 15*time.Minute)
	for _, svc := range []string{"", "wrong"} {
		s, resp := e.post(t, "/internal/v1/connector-enrollments", svc, map[string]string{
			"token": token, "csr_pem": makeCSR(t),
		})
		if s != 401 || resp["code"] != "AR_SVC_UNAUTHENTICATED" {
			t.Fatalf("svc token %q: %d %v", svc, s, resp)
		}
	}
}

func TestHandshakeFingerprintBinding(t *testing.T) {
	e := newEnv(t)
	id, token := e.createConnector(t, 15*time.Minute)
	_, resp := e.post(t, "/internal/v1/connector-enrollments", svcToken, map[string]string{
		"token": token, "csr_pem": makeCSR(t),
	})
	certPEM := resp["certificate_pem"].(string)
	fp, _ := pki.Fingerprint([]byte(certPEM))

	// 正确指纹 → 204
	if s, _ := e.post(t, "/internal/v1/connector-handshakes", svcToken, map[string]string{
		"tenant_id": devTenantID, "connector_id": id, "fingerprint": fp,
	}); s != 204 {
		t.Fatalf("valid handshake status %d", s)
	}
	// 错误指纹 → 403
	if s, r := e.post(t, "/internal/v1/connector-handshakes", svcToken, map[string]string{
		"tenant_id": devTenantID, "connector_id": id, "fingerprint": "deadbeef",
	}); s != 403 || r["code"] != "AR_AUTH_FORBIDDEN" {
		t.Fatalf("bad fingerprint: %d %v", s, r)
	}
}

func TestStatusUpdateAndRevokedGuard(t *testing.T) {
	e := newEnv(t)
	id, token := e.createConnector(t, 15*time.Minute)
	e.post(t, "/internal/v1/connector-enrollments", svcToken, map[string]string{
		"token": token, "csr_pem": makeCSR(t),
	})

	if s, _ := e.post(t, "/internal/v1/connector-status", svcToken, map[string]any{
		"tenant_id": devTenantID, "connector_id": id, "status": "online",
	}); s != 204 {
		t.Fatalf("status online %d", s)
	}

	// 吊销后状态写入不得复活
	if _, err := e.admin.Exec(`UPDATE connectors SET status='revoked' WHERE id=$1`, id); err != nil {
		t.Fatalf("revoke: %v", err)
	}
	e.post(t, "/internal/v1/connector-status", svcToken, map[string]any{
		"tenant_id": devTenantID, "connector_id": id, "status": "online",
	})
	var st string
	_ = e.admin.QueryRow(`SELECT status FROM connectors WHERE id=$1`, id).Scan(&st)
	if st != "revoked" {
		t.Fatalf("revoked connector revived to %q", st)
	}
}

// --- helpers ---

func makeCSR(t *testing.T) string {
	t.Helper()
	key, _ := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	der, err := x509.CreateCertificateRequest(rand.Reader,
		&x509.CertificateRequest{Subject: pkix.Name{CommonName: "pending"}}, key)
	if err != nil {
		t.Fatalf("csr: %v", err)
	}
	return string(pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE REQUEST", Bytes: der}))
}

func randSuffix(t *testing.T) string {
	t.Helper()
	b := make([]byte, 6)
	if _, err := rand.Read(b); err != nil {
		t.Fatalf("rand: %v", err)
	}
	const hexd = "0123456789abcdef"
	out := make([]byte, 12)
	for i, v := range b {
		out[i*2] = hexd[v>>4]
		out[i*2+1] = hexd[v&0xf]
	}
	return string(out)
}

// TestIngestOwnershipEnforcedByDB spec-1.5 review 补：归属校验走真库——
// 连接器只能替自己名下的 connector 数据源上报；同租户内别的连接器的数据源、
// direct 数据源、不存在的数据源，一律拒。单测里是替身，这里是生产实现 repoOwnership。
func TestIngestOwnershipEnforcedByDB(t *testing.T) {
	e := newEnv(t)
	connA, _ := e.createConnector(t, 15*time.Minute)
	connB, _ := e.createConnector(t, 15*time.Minute)

	ctx := tenancy.WithTenant(context.Background(), devTenantID)
	var dsOfA, dsOfB, dsDirect string
	err := e.store.InTenantTx(ctx, func(ctx context.Context, tx repo.Tx) error {
		mk := func(name, mode string, connID *string) (string, error) {
			in := repo.DatasourceInput{
				Name: name, EngineFamily: "postgres", Engine: "postgres",
				ConnectMode: mode, ConnectorID: connID, Host: "h", Port: 5432,
			}
			if mode == "direct" {
				credID, err := repo.InsertCredential(ctx, tx, "u", []byte{0}, "k1")
				if err != nil {
					return "", err
				}
				in.CredentialID = &credID
			}
			ds, err := repo.InsertDatasource(ctx, tx, in)
			return ds.ID, err
		}
		var err error
		if dsOfA, err = mk("ds-a", "connector", &connA); err != nil {
			return err
		}
		if dsOfB, err = mk("ds-b", "connector", &connB); err != nil {
			return err
		}
		dsDirect, err = mk("ds-direct", "direct", nil)
		return err
	})
	if err != nil {
		t.Fatalf("seed datasources: %v", err)
	}

	// 上报用替身落点，只验归属这一层
	sink := metrics.NewBufferSink(8)
	srv := httptest.NewServer(svcapi.New(e.store, nil, svcToken).WithSinks(sink, sink).Handler())
	t.Cleanup(srv.Close)
	e2 := &env{admin: e.admin, store: e.store, srv: srv}

	body := func(connID, dsID string) map[string]any {
		return map[string]any{
			"tenant_id": devTenantID, "connector_id": connID,
			"batch": map[string]any{
				"datasource_id": dsID, "engine_family": "postgres",
				"metrics": []map[string]any{{"name": "db.connections.active", "value": 1, "at": time.Now().UTC()}},
			},
		}
	}
	cases := []struct {
		name       string
		connID, ds string
		wantStatus int
		wantCode   string
	}{
		{"自己名下的数据源 → 收", connA, dsOfA, http.StatusAccepted, ""},
		{"同租户另一连接器的数据源 → 拒", connA, dsOfB, http.StatusForbidden, "AR_COLLECT_DATASOURCE_MISMATCH"},
		{"direct 数据源 → 拒", connA, dsDirect, http.StatusForbidden, "AR_COLLECT_DATASOURCE_MISMATCH"},
		{"不存在的数据源 → 404", connA, "ffffffff-0000-0000-0000-00000000000f", http.StatusNotFound, "AR_DATASOURCE_NOT_FOUND"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			status, m := e2.post(t, "/internal/v1/collected/metrics", svcToken, body(c.connID, c.ds))
			if status != c.wantStatus {
				t.Fatalf("status = %d, want %d（%v）", status, c.wantStatus, m)
			}
			if c.wantCode != "" && m["code"] != c.wantCode {
				t.Fatalf("code = %v, want %s", m["code"], c.wantCode)
			}
		})
	}
	if sink.Total() != 1 {
		t.Fatalf("落点收到 %d 批，want 1——被拒的上报不该落库", sink.Total())
	}
}

// TestLLMQuotaCheckAndUsage spec-1.7 T18（内部面）：quota-check 三态（有余额 / 超额 429 /
// 无配额行 = 不限）；usage 记账幂等（同 idem_key 重复上报不双记）；PUT 下调预算后立即拒。
func TestLLMQuotaCheckAndUsage(t *testing.T) {
	e := newEnv(t)
	post := func(path string, body any) (int, map[string]any) { return e.post(t, path, svcToken, body) }
	usage := func(idem string, tokens int, status string) map[string]any {
		return map[string]any{
			"tenant_id": devTenantID, "idem_key": idem, "status": status,
			"usage": map[string]any{
				"model": "chat-default", "upstream_model": "deepseek-chat",
				"prompt_tokens": tokens, "completion_tokens": 0, "total_tokens": tokens, "stream": false,
			},
		}
	}

	// ① dev 租户 seed 预算 5 千万，未用 → 200 有余额
	st, m := post("/internal/v1/llm/quota-check", map[string]any{"tenant_id": devTenantID})
	if st != 200 || m["budget"] != float64(50_000_000) || m["used"] != float64(0) {
		t.Fatalf("quota-check initial: %d %v", st, m)
	}

	// ② 记账 + 幂等：同 idem_key 三次只算一次
	for i := 0; i < 3; i++ {
		if st, m := post("/internal/v1/llm/usage", usage("idem-1", 1000, "ok")); st != 202 {
			t.Fatalf("usage #%d: %d %v", i, st, m)
		}
	}
	st, m = post("/internal/v1/llm/quota-check", map[string]any{"tenant_id": devTenantID})
	if st != 200 || m["used"] != float64(1000) {
		t.Fatalf("used after idempotent records = %v（want 1000，重复上报双记了）", m["used"])
	}
	// upstream_error / quota_rejected 不计入已用
	if st, _ := post("/internal/v1/llm/usage", usage("idem-2", 5000, "upstream_error")); st != 202 {
		t.Fatalf("usage upstream_error: %d", st)
	}
	st, m = post("/internal/v1/llm/quota-check", map[string]any{"tenant_id": devTenantID})
	if m["used"] != float64(1000) {
		t.Fatalf("upstream_error 被计入已用: %v", m)
	}

	// ③ 预算下调到 500（< 已用 1000）→ 立即 429
	ctx := tenancy.WithTenant(context.Background(), devTenantID)
	if err := e.store.InTenantTx(ctx, func(ctx context.Context, tx repo.Tx) error {
		_, err := repo.UpsertLLMQuota(ctx, tx, 500, true)
		return err
	}); err != nil {
		t.Fatalf("lower quota: %v", err)
	}
	st, m = post("/internal/v1/llm/quota-check", map[string]any{"tenant_id": devTenantID})
	if st != 429 || m["code"] != "AR_QUOTA_EXCEEDED" {
		t.Fatalf("超额应 429 AR_QUOTA_EXCEEDED，实际 %d %v", st, m)
	}
	// hard_stop=false → 超额也放行
	if err := e.store.InTenantTx(ctx, func(ctx context.Context, tx repo.Tx) error {
		_, err := repo.UpsertLLMQuota(ctx, tx, 500, false)
		return err
	}); err != nil {
		t.Fatalf("soft quota: %v", err)
	}
	if st, _ = post("/internal/v1/llm/quota-check", map[string]any{"tenant_id": devTenantID}); st != 200 {
		t.Fatalf("hard_stop=false 超额应放行，实际 %d", st)
	}

	// ④ 无配额行的租户 → 不限（budget=-1）
	if _, err := e.admin.Exec(`INSERT INTO tenants (id, name, slug) VALUES ($1, '租户B', 'tenant-b') ON CONFLICT DO NOTHING`,
		"22222222-2222-2222-2222-222222222222"); err != nil {
		t.Fatalf("seed tenant B: %v", err)
	}
	st, m = post("/internal/v1/llm/quota-check", map[string]any{"tenant_id": "22222222-2222-2222-2222-222222222222"})
	if st != 200 || m["budget"] != float64(-1) {
		t.Fatalf("no-quota tenant: %d %v（want 200 budget=-1）", st, m)
	}

	// ⑤ 载荷校验：非法 status / 负 token / 缺 idem_key
	if st, _ := post("/internal/v1/llm/usage", usage("idem-3", 1, "bogus")); st != 400 {
		t.Fatalf("bogus status: %d", st)
	}
	if st, _ := post("/internal/v1/llm/usage", usage("idem-4", -1, "ok")); st != 400 {
		t.Fatalf("negative tokens: %d", st)
	}
	if st, _ := post("/internal/v1/llm/usage", usage("", 1, "ok")); st != 400 {
		t.Fatalf("missing idem: %d", st)
	}
	// 无 svc token
	if st, _ := e.post(t, "/internal/v1/llm/quota-check", "", map[string]any{"tenant_id": devTenantID}); st != 401 {
		t.Fatalf("no svc token: %d", st)
	}
}
