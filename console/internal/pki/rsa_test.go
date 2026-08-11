package pki

import (
	"crypto/rand"
	"crypto/rsa"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/pem"
	"math/big"
	"testing"
	"time"
)

// TestLoadRSAPKCS1CA 覆盖 Helm genCA 产物（RSA PKCS1 键）的装载与签发——
// 与 EC 路径等价，防止部署侧密钥格式回归（dev-verify 曾因此 crashloop）。
func TestLoadRSAPKCS1CA(t *testing.T) {
	t.Parallel()

	key, err := rsa.GenerateKey(rand.Reader, 2048)
	if err != nil {
		t.Fatalf("rsa key: %v", err)
	}
	tmpl := &x509.Certificate{
		SerialNumber:          big.NewInt(1),
		Subject:               pkix.Name{CommonName: "helm-style-ca"},
		NotBefore:             time.Now().Add(-time.Minute),
		NotAfter:              time.Now().Add(time.Hour),
		IsCA:                  true,
		KeyUsage:              x509.KeyUsageCertSign,
		BasicConstraintsValid: true,
	}
	der, err := x509.CreateCertificate(rand.Reader, tmpl, tmpl, &key.PublicKey, key)
	if err != nil {
		t.Fatalf("ca cert: %v", err)
	}
	certPEM := pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: der})
	keyPEM := pem.EncodeToMemory(&pem.Block{Type: "RSA PRIVATE KEY", Bytes: x509.MarshalPKCS1PrivateKey(key)})

	ca, err := Load(certPEM, keyPEM)
	if err != nil {
		t.Fatalf("load RSA CA: %v", err)
	}
	// 用 RSA CA 签发一张客户端证书（EC CSR），链校验通过
	leafPEM, _, err := ca.SignCSR(testCSR(t, "attacker"), "conn-1", "tenant-1", time.Hour)
	if err != nil {
		t.Fatalf("sign with RSA CA: %v", err)
	}
	roots := x509.NewCertPool()
	roots.AppendCertsFromPEM(ca.BundlePEM())
	block, _ := pem.Decode(leafPEM)
	leaf, _ := x509.ParseCertificate(block.Bytes)
	if _, err := leaf.Verify(x509.VerifyOptions{
		Roots: roots, KeyUsages: []x509.ExtKeyUsage{x509.ExtKeyUsageClientAuth},
	}); err != nil {
		t.Fatalf("verify leaf under RSA CA: %v", err)
	}
}
