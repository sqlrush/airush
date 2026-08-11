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
