// Package credcrypto 是直连模式凭据的信封加密实现（spec-1.1 D5，AD-4②）。
// 布局（enc_version=1，全部定长偏移）：
//
//	blob = nonce1(12) ‖ wrappedDEK(48) ‖ nonce2(12) ‖ AES-256-GCM(DEK, plaintext)
//
// KEK 经环境变量注入（base64 32B，k8s Secret 承载）；DEK 每凭据随机；
// key_id 标识 KEK 版本——轮换时仅重包 DEK 层，不动数据层。
// 唯一解密消费方是直连接入器路径（spec-1.17）；console API 永不返回明文。
package credcrypto

import (
	"crypto/aes"
	"crypto/cipher"
	"crypto/rand"
	"encoding/base64"
	"errors"
	"fmt"
)

const (
	kekLen        = 32
	dekLen        = 32
	nonceLen      = 12
	wrappedDEKLen = dekLen + 16 // GCM tag 16B
	headerLen     = nonceLen + wrappedDEKLen + nonceLen
)

// ErrMalformedBlob 表示密文结构不完整（长度不足以容纳定长头部）。
var ErrMalformedBlob = errors.New("credcrypto: malformed ciphertext blob")

// Sealer 持有 KEK 的 AEAD 与版本标识；不可变，可并发使用。
type Sealer struct {
	kekAEAD cipher.AEAD
	keyID   string
}

// New 校验并装载 KEK（base64 32B）；keyID 是该 KEK 的版本标识（如 "v1"）。
func New(kekBase64, keyID string) (*Sealer, error) {
	if keyID == "" {
		return nil, errors.New("credcrypto: key id must not be empty")
	}
	kek, err := base64.StdEncoding.DecodeString(kekBase64)
	if err != nil {
		return nil, fmt.Errorf("credcrypto: decode KEK: %w", err)
	}
	if len(kek) != kekLen {
		return nil, fmt.Errorf("credcrypto: KEK must be %d bytes, got %d", kekLen, len(kek))
	}
	aead, err := newAEAD(kek)
	if err != nil {
		return nil, err
	}
	return &Sealer{kekAEAD: aead, keyID: keyID}, nil
}

// KeyID 返回 KEK 版本标识（落 datasource_credentials.key_id）。
func (s *Sealer) KeyID() string { return s.keyID }

// Seal 信封加密：新 DEK 加密明文，KEK 包裹 DEK。
func (s *Sealer) Seal(plaintext []byte) ([]byte, error) {
	dek := make([]byte, dekLen)
	if _, err := rand.Read(dek); err != nil {
		return nil, fmt.Errorf("credcrypto: generate DEK: %w", err)
	}
	dekAEAD, err := newAEAD(dek)
	if err != nil {
		return nil, err
	}

	nonce1 := make([]byte, nonceLen)
	nonce2 := make([]byte, nonceLen)
	for _, n := range [][]byte{nonce1, nonce2} {
		if _, err := rand.Read(n); err != nil {
			return nil, fmt.Errorf("credcrypto: generate nonce: %w", err)
		}
	}

	blob := make([]byte, 0, headerLen+len(plaintext)+16)
	blob = append(blob, nonce1...)
	blob = s.kekAEAD.Seal(blob, nonce1, dek, nil)
	blob = append(blob, nonce2...)
	blob = dekAEAD.Seal(blob, nonce2, plaintext, nil)
	return blob, nil
}

// Open 解封；任何一层认证失败或结构不完整都返回错误（不区分细节，防 oracle）。
func (s *Sealer) Open(blob []byte) ([]byte, error) {
	if len(blob) < headerLen {
		return nil, ErrMalformedBlob
	}
	nonce1 := blob[:nonceLen]
	wrapped := blob[nonceLen : nonceLen+wrappedDEKLen]
	nonce2 := blob[nonceLen+wrappedDEKLen : headerLen]
	ct := blob[headerLen:]

	dek, err := s.kekAEAD.Open(nil, nonce1, wrapped, nil)
	if err != nil {
		return nil, fmt.Errorf("credcrypto: unwrap DEK: %w", err)
	}
	dekAEAD, err := newAEAD(dek)
	if err != nil {
		return nil, err
	}
	plaintext, err := dekAEAD.Open(nil, nonce2, ct, nil)
	if err != nil {
		return nil, fmt.Errorf("credcrypto: open payload: %w", err)
	}
	return plaintext, nil
}

func newAEAD(key []byte) (cipher.AEAD, error) {
	block, err := aes.NewCipher(key)
	if err != nil {
		return nil, fmt.Errorf("credcrypto: new cipher: %w", err)
	}
	aead, err := cipher.NewGCM(block)
	if err != nil {
		return nil, fmt.Errorf("credcrypto: new GCM: %w", err)
	}
	return aead, nil
}
