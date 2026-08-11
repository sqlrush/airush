package enrolltoken

import (
	"strings"
	"testing"
)

func TestNewParseRoundTrip(t *testing.T) {
	t.Parallel()

	token, hash, err := New("tenant-1", "conn-1")
	if err != nil {
		t.Fatalf("new: %v", err)
	}
	parsed, err := Parse(token)
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	if parsed.TenantID != "tenant-1" || parsed.ConnectorID != "conn-1" {
		t.Fatalf("parsed = %+v", parsed)
	}
	if !parsed.Matches(hash) {
		t.Fatal("secret does not match its own hash")
	}
	if parsed.Matches(HashSecret("wrong")) {
		t.Fatal("wrong hash matched")
	}
}

func TestTokensAreUnique(t *testing.T) {
	t.Parallel()
	a, _, _ := New("t", "c")
	b, _, _ := New("t", "c")
	if a == b {
		t.Fatal("two tokens identical")
	}
}

func TestParseRejectsMalformed(t *testing.T) {
	t.Parallel()
	for _, bad := range []string{"", "nodot", "!!!.secret", "bm90LWNvbG9u.s", strings.Repeat("a", 10) + "."} {
		if _, err := Parse(bad); err == nil {
			t.Fatalf("token %q accepted", bad)
		}
	}
}
