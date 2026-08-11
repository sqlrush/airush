// Package enrolltoken 定义一次性注册令牌格式（spec-1.2 §2.3）。
// 形态：base64url(tenant_id ":" connector_id) "." hex(32B secret)
// 租户/接入器随牌自携——svcapi 校验时据此进入租户上下文执行 RLS 事务，
// 控制面读写不需要任何跨租户旁路。库中仅存 sha256(secret)。
package enrolltoken

import (
	"crypto/rand"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/base64"
	"encoding/hex"
	"errors"
	"fmt"
	"strings"
)

const secretLen = 32

// Token 是解析后的令牌三元组。
type Token struct {
	TenantID    string
	ConnectorID string
	secret      string
}

// New 生成新令牌；返回完整令牌串（仅此一次出明文）与 secret 哈希（落库）。
func New(tenantID, connectorID string) (token string, secretHash string, err error) {
	buf := make([]byte, secretLen)
	if _, err := rand.Read(buf); err != nil {
		return "", "", fmt.Errorf("enrolltoken: rand: %w", err)
	}
	secret := hex.EncodeToString(buf)
	addr := base64.RawURLEncoding.EncodeToString([]byte(tenantID + ":" + connectorID))
	return addr + "." + secret, HashSecret(secret), nil
}

// Parse 解析令牌串（不校验哈希——哈希比对在租户事务内做）。
func Parse(token string) (Token, error) {
	addr, secret, ok := strings.Cut(token, ".")
	if !ok || secret == "" {
		return Token{}, errors.New("enrolltoken: malformed token")
	}
	raw, err := base64.RawURLEncoding.DecodeString(addr)
	if err != nil {
		return Token{}, fmt.Errorf("enrolltoken: decode addr: %w", err)
	}
	tenantID, connectorID, ok := strings.Cut(string(raw), ":")
	if !ok || tenantID == "" || connectorID == "" {
		return Token{}, errors.New("enrolltoken: addr structure invalid")
	}
	return Token{TenantID: tenantID, ConnectorID: connectorID, secret: secret}, nil
}

// HashSecret 计算 secret 的存储哈希。
func HashSecret(secret string) string {
	sum := sha256.Sum256([]byte(secret))
	return hex.EncodeToString(sum[:])
}

// Matches 常数时间比对令牌 secret 与库中哈希。
func (t Token) Matches(storedHash string) bool {
	h := HashSecret(t.secret)
	return subtle.ConstantTimeCompare([]byte(h), []byte(storedHash)) == 1
}
