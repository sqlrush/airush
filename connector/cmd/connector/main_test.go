package main

import (
	"regexp"
	"testing"
)

// TestVersionFormat 冒烟：版本号必须为 semver 形态（spec-0.1 T3 的单测面）。
func TestVersionFormat(t *testing.T) {
	t.Parallel()
	re := regexp.MustCompile(`^\d+\.\d+\.\d+`)
	if !re.MatchString(version) {
		t.Fatalf("version %q is not semver-like", version)
	}
}
