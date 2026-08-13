package main

import (
	"regexp"
	"testing"
)

// TestBuildDirectBadKEK：非法 KEK → 凭据加密器构造失败（fail-fast，不静默）。
func TestBuildDirectBadKEK(t *testing.T) {
	t.Parallel()
	_, _, err := buildDirect(appConfig{CredentialKEK: "", CredentialKEKID: "v1"}, nil)
	if err == nil {
		t.Fatal("empty KEK should fail buildDirect")
	}
}

// TestVersionFormat 冒烟：版本号必须为 semver 形态（spec-0.1 T3 的单测面）。
func TestVersionFormat(t *testing.T) {
	t.Parallel()
	re := regexp.MustCompile(`^\d+\.\d+\.\d+`)
	if !re.MatchString(version) {
		t.Fatalf("version %q is not semver-like", version)
	}
}
