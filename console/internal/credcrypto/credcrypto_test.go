package credcrypto

import (
	"bytes"
	"crypto/rand"
	"encoding/base64"
	"strings"
	"testing"
)

func testKEK(t *testing.T) string {
	t.Helper()
	kek := make([]byte, 32)
	if _, err := rand.Read(kek); err != nil {
		t.Fatalf("rand: %v", err)
	}
	return base64.StdEncoding.EncodeToString(kek)
}

// TestSealOpenRoundTrip spec-1.1 T4（单测面）：roundtrip 一致且密文不含明文子串。
func TestSealOpenRoundTrip(t *testing.T) {
	t.Parallel()

	s, err := New(testKEK(t), "v1")
	if err != nil {
		t.Fatalf("new: %v", err)
	}
	plaintext := []byte("super-secret-db-password")

	blob, err := s.Seal(plaintext)
	if err != nil {
		t.Fatalf("seal: %v", err)
	}
	if bytes.Contains(blob, plaintext) {
		t.Fatal("ciphertext contains plaintext substring")
	}

	got, err := s.Open(blob)
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	if !bytes.Equal(got, plaintext) {
		t.Fatalf("roundtrip mismatch: %q", got)
	}
	if s.KeyID() != "v1" {
		t.Fatalf("key id = %q, want v1", s.KeyID())
	}
}

// TestSealDEKUniquePerCall 每次 Seal 使用独立 DEK/nonce：同明文两次密文不同。
func TestSealDEKUniquePerCall(t *testing.T) {
	t.Parallel()

	s, err := New(testKEK(t), "v1")
	if err != nil {
		t.Fatalf("new: %v", err)
	}
	b1, err1 := s.Seal([]byte("same"))
	b2, err2 := s.Seal([]byte("same"))
	if err1 != nil || err2 != nil {
		t.Fatalf("seal: %v / %v", err1, err2)
	}
	if bytes.Equal(b1, b2) {
		t.Fatal("two seals of same plaintext produced identical blobs")
	}
}

func TestOpenRejectsTamperAndWrongKEK(t *testing.T) {
	t.Parallel()

	s, err := New(testKEK(t), "v1")
	if err != nil {
		t.Fatalf("new: %v", err)
	}
	blob, err := s.Seal([]byte("payload"))
	if err != nil {
		t.Fatalf("seal: %v", err)
	}

	tampered := append([]byte(nil), blob...)
	tampered[len(tampered)-1] ^= 0xff
	if _, err := s.Open(tampered); err == nil {
		t.Fatal("tampered blob accepted")
	}

	other, err := New(testKEK(t), "v2")
	if err != nil {
		t.Fatalf("new other: %v", err)
	}
	if _, err := other.Open(blob); err == nil {
		t.Fatal("wrong KEK accepted")
	}

	if _, err := s.Open(blob[:headerLen-1]); err == nil {
		t.Fatal("truncated blob accepted")
	}
}

func TestNewValidatesInput(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name  string
		kek   string
		keyID string
		want  string
	}{
		{"非法 base64", "!!!", "v1", "decode KEK"},
		{"长度不足", base64.StdEncoding.EncodeToString(make([]byte, 16)), "v1", "32 bytes"},
		{"空 key id", testKEK(t), "", "key id"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			_, err := New(tt.kek, tt.keyID)
			if err == nil || !strings.Contains(err.Error(), tt.want) {
				t.Fatalf("err = %v, want contains %q", err, tt.want)
			}
		})
	}
}
