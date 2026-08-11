// Package pki 是平台内部 CA（spec-1.2 D3）：Connector 客户端证书的唯一签发方。
// CA 键经部署侧 Secret 注入（PEM）；证书 CN=connector_id、SAN URI 携带租户，
// 90 天有效期由调用方传入；吊销即 DB 状态（无 CRL 分发，会话握手时校验）。
package pki

import (
	"crypto"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/sha256"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/hex"
	"encoding/pem"
	"errors"
	"fmt"
	"math/big"
	"net/url"
	"time"
)

// notBeforeBackdate 抵御客户侧时钟偏移（spec-1.2 §6）。
const notBeforeBackdate = 5 * time.Minute

// CA 持有签发身份；不可变，可并发使用。
type CA struct {
	cert    *x509.Certificate
	signer  crypto.Signer
	certPEM []byte
}

// Load 从 PEM 装载 CA（部署侧 Secret 注入路径）。
func Load(certPEM, keyPEM []byte) (*CA, error) {
	certBlock, _ := pem.Decode(certPEM)
	if certBlock == nil || certBlock.Type != "CERTIFICATE" {
		return nil, errors.New("pki: CA cert PEM invalid")
	}
	cert, err := x509.ParseCertificate(certBlock.Bytes)
	if err != nil {
		return nil, fmt.Errorf("pki: parse CA cert: %w", err)
	}
	if !cert.IsCA {
		return nil, errors.New("pki: certificate is not a CA")
	}
	keyBlock, _ := pem.Decode(keyPEM)
	if keyBlock == nil {
		return nil, errors.New("pki: CA key PEM invalid")
	}
	signer, err := parsePrivateKey(keyBlock.Bytes)
	if err != nil {
		return nil, fmt.Errorf("pki: parse CA key: %w", err)
	}
	return &CA{cert: cert, signer: signer, certPEM: append([]byte(nil), certPEM...)}, nil
}

// Generate 生成新 CA（`console pki-init` 与测试用；10 年，ECDSA P-256）。
func Generate(commonName string) (certPEM, keyPEM []byte, err error) {
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		return nil, nil, fmt.Errorf("pki: generate CA key: %w", err)
	}
	serial, err := randomSerial()
	if err != nil {
		return nil, nil, err
	}
	tmpl := &x509.Certificate{
		SerialNumber:          serial,
		Subject:               pkix.Name{CommonName: commonName},
		NotBefore:             time.Now().Add(-notBeforeBackdate),
		NotAfter:              time.Now().AddDate(10, 0, 0),
		IsCA:                  true,
		KeyUsage:              x509.KeyUsageCertSign | x509.KeyUsageDigitalSignature,
		BasicConstraintsValid: true,
		MaxPathLenZero:        true,
	}
	der, err := x509.CreateCertificate(rand.Reader, tmpl, tmpl, &key.PublicKey, key)
	if err != nil {
		return nil, nil, fmt.Errorf("pki: create CA cert: %w", err)
	}
	keyDER, err := x509.MarshalECPrivateKey(key)
	if err != nil {
		return nil, nil, fmt.Errorf("pki: marshal CA key: %w", err)
	}
	return pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: der}),
		pem.EncodeToMemory(&pem.Block{Type: "EC PRIVATE KEY", Bytes: keyDER}), nil
}

// SignCSR 校验 CSR 并签发客户端证书：CN 强制改写为 connectorID（不信任 CSR 主体），
// SAN URI 绑定租户。返回证书 PEM 与指纹（落 connectors.cert_fingerprint）。
func (ca *CA) SignCSR(csrPEM []byte, connectorID, tenantID string, ttl time.Duration) (certPEM []byte, fingerprint string, err error) {
	block, _ := pem.Decode(csrPEM)
	if block == nil || block.Type != "CERTIFICATE REQUEST" {
		return nil, "", errors.New("pki: CSR PEM invalid")
	}
	csr, err := x509.ParseCertificateRequest(block.Bytes)
	if err != nil {
		return nil, "", fmt.Errorf("pki: parse CSR: %w", err)
	}
	if err := csr.CheckSignature(); err != nil {
		return nil, "", fmt.Errorf("pki: CSR signature invalid: %w", err)
	}
	serial, err := randomSerial()
	if err != nil {
		return nil, "", err
	}
	tmpl := &x509.Certificate{
		SerialNumber: serial,
		Subject:      pkix.Name{CommonName: connectorID},
		URIs: []*url.URL{{
			Scheme: "airush",
			Host:   "tenant",
			Path:   "/" + tenantID + "/connector/" + connectorID,
		}},
		NotBefore:   time.Now().Add(-notBeforeBackdate),
		NotAfter:    time.Now().Add(ttl),
		KeyUsage:    x509.KeyUsageDigitalSignature,
		ExtKeyUsage: []x509.ExtKeyUsage{x509.ExtKeyUsageClientAuth},
	}
	der, err := x509.CreateCertificate(rand.Reader, tmpl, ca.cert, csr.PublicKey, ca.signer)
	if err != nil {
		return nil, "", fmt.Errorf("pki: sign certificate: %w", err)
	}
	sum := sha256.Sum256(der)
	return pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: der}),
		hex.EncodeToString(sum[:]), nil
}

// BundlePEM 返回信任链（客户端校验会话端、网关校验客户端共用）。
func (ca *CA) BundlePEM() []byte { return append([]byte(nil), ca.certPEM...) }

// Fingerprint 计算证书 PEM 的 SHA-256（DER）指纹，与 SignCSR 返回一致。
func Fingerprint(certPEM []byte) (string, error) {
	block, _ := pem.Decode(certPEM)
	if block == nil || block.Type != "CERTIFICATE" {
		return "", errors.New("pki: cert PEM invalid")
	}
	sum := sha256.Sum256(block.Bytes)
	return hex.EncodeToString(sum[:]), nil
}

// parsePrivateKey 兼容 EC（pki-init 产物）与 RSA/PKCS8（Helm genCA 产物）；
// 返回 crypto.Signer，签发路径对密钥类型无关。
func parsePrivateKey(der []byte) (crypto.Signer, error) {
	if k, err := x509.ParsePKCS8PrivateKey(der); err == nil {
		if s, ok := k.(crypto.Signer); ok {
			return s, nil
		}
		return nil, errors.New("pki: PKCS8 key is not a signer")
	}
	if k, err := x509.ParseECPrivateKey(der); err == nil {
		return k, nil
	}
	if k, err := x509.ParsePKCS1PrivateKey(der); err == nil {
		return k, nil
	}
	return nil, errors.New("pki: unsupported private key format")
}

func randomSerial() (*big.Int, error) {
	serial, err := rand.Int(rand.Reader, new(big.Int).Lsh(big.NewInt(1), 128))
	if err != nil {
		return nil, fmt.Errorf("pki: random serial: %w", err)
	}
	return serial, nil
}
