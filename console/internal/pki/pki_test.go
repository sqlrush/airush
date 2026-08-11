package pki

import (
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/pem"
	"strings"
	"testing"
	"time"
)

func testCSR(t *testing.T, cn string) []byte {
	t.Helper()
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatalf("gen key: %v", err)
	}
	der, err := x509.CreateCertificateRequest(rand.Reader,
		&x509.CertificateRequest{Subject: pkix.Name{CommonName: cn}}, key)
	if err != nil {
		t.Fatalf("create csr: %v", err)
	}
	return pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE REQUEST", Bytes: der})
}

func TestGenerateLoadSignVerify(t *testing.T) {
	t.Parallel()

	certPEM, keyPEM, err := Generate("airush-connector-ca")
	if err != nil {
		t.Fatalf("generate: %v", err)
	}
	ca, err := Load(certPEM, keyPEM)
	if err != nil {
		t.Fatalf("load: %v", err)
	}

	// CSR 主体 CN 故意与 connectorID 不同——签发必须强制改写
	leafPEM, fp, err := ca.SignCSR(testCSR(t, "attacker-chosen-cn"),
		"conn-123", "tenant-abc", 90*24*time.Hour)
	if err != nil {
		t.Fatalf("sign: %v", err)
	}

	block, _ := pem.Decode(leafPEM)
	leaf, err := x509.ParseCertificate(block.Bytes)
	if err != nil {
		t.Fatalf("parse leaf: %v", err)
	}
	if leaf.Subject.CommonName != "conn-123" {
		t.Fatalf("CN = %q, want conn-123 (CSR 主体不可信)", leaf.Subject.CommonName)
	}
	if len(leaf.URIs) != 1 || !strings.Contains(leaf.URIs[0].String(), "tenant-abc") ||
		!strings.Contains(leaf.URIs[0].String(), "conn-123") {
		t.Fatalf("SAN URIs = %v, want tenant+connector 绑定", leaf.URIs)
	}
	if !leaf.NotBefore.Before(time.Now().Add(-4 * time.Minute)) {
		t.Fatalf("NotBefore %v not backdated", leaf.NotBefore)
	}

	// 链校验：leaf 由该 CA 签出
	roots := x509.NewCertPool()
	if !roots.AppendCertsFromPEM(ca.BundlePEM()) {
		t.Fatal("append CA bundle")
	}
	if _, err := leaf.Verify(x509.VerifyOptions{
		Roots:     roots,
		KeyUsages: []x509.ExtKeyUsage{x509.ExtKeyUsageClientAuth},
	}); err != nil {
		t.Fatalf("chain verify: %v", err)
	}

	// 指纹一致性：SignCSR 返回值 == Fingerprint(PEM)
	fp2, err := Fingerprint(leafPEM)
	if err != nil || fp2 != fp {
		t.Fatalf("fingerprint mismatch: %s vs %s (err=%v)", fp, fp2, err)
	}
}

func TestSignRejectsBadCSR(t *testing.T) {
	t.Parallel()

	certPEM, keyPEM, err := Generate("ca")
	if err != nil {
		t.Fatalf("generate: %v", err)
	}
	ca, err := Load(certPEM, keyPEM)
	if err != nil {
		t.Fatalf("load: %v", err)
	}

	if _, _, err := ca.SignCSR([]byte("not-pem"), "c", "t", time.Hour); err == nil {
		t.Fatal("non-PEM CSR accepted")
	}

	// 篡改 CSR 签名：改 DER 中间字节后重封 PEM
	good := testCSR(t, "x")
	block, _ := pem.Decode(good)
	block.Bytes[len(block.Bytes)-10] ^= 0xff
	tampered := pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE REQUEST", Bytes: block.Bytes})
	if _, _, err := ca.SignCSR(tampered, "c", "t", time.Hour); err == nil {
		t.Fatal("tampered CSR accepted")
	}
}

func TestLoadValidatesInput(t *testing.T) {
	t.Parallel()

	certPEM, keyPEM, err := Generate("ca")
	if err != nil {
		t.Fatalf("generate: %v", err)
	}
	if _, err := Load([]byte("junk"), keyPEM); err == nil {
		t.Fatal("junk cert accepted")
	}
	if _, err := Load(certPEM, []byte("junk")); err == nil {
		t.Fatal("junk key accepted")
	}

	// 非 CA 证书拒绝：用 CA 签一张 leaf 再当 CA 装载
	ca, _ := Load(certPEM, keyPEM)
	leafPEM, _, err := ca.SignCSR(testCSR(t, "x"), "c", "t", time.Hour)
	if err != nil {
		t.Fatalf("sign: %v", err)
	}
	if _, err := Load(leafPEM, keyPEM); err == nil {
		t.Fatal("non-CA cert accepted as CA")
	}
}
