package accept

import (
	"crypto/sha256"
	"crypto/x509"
	"encoding/hex"
	"errors"
	"strings"
)

// tenantFromCert 从客户端证书 SAN URI 提取租户 id（pki 签发格式：
// airush://tenant/<tenantID>/connector/<connectorID>）。
func tenantFromCert(cert *x509.Certificate) (string, error) {
	for _, u := range cert.URIs {
		if u.Scheme != "airush" || u.Host != "tenant" {
			continue
		}
		parts := strings.Split(strings.Trim(u.Path, "/"), "/")
		if len(parts) >= 3 && parts[1] == "connector" && parts[0] != "" {
			return parts[0], nil
		}
	}
	return "", errors.New("accept: no tenant SAN in client certificate")
}

// certFingerprint 计算证书 DER 的 SHA-256（与 console pki.SignCSR 返回一致）。
func certFingerprint(cert *x509.Certificate) string {
	sum := sha256.Sum256(cert.Raw)
	return hex.EncodeToString(sum[:])
}
