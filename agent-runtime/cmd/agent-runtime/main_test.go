package main

import (
	"regexp"
	"strings"
	"testing"
	"time"
)

// TestVersionFormat 冒烟：版本号必须为 semver 形态（spec-0.1 T3 的单测面）。
func TestVersionFormat(t *testing.T) {
	t.Parallel()
	re := regexp.MustCompile(`^\d+\.\d+\.\d+`)
	if !re.MatchString(version) {
		t.Fatalf("version %q is not semver-like", version)
	}
}

func TestParseMCPEndpoints(t *testing.T) {
	t.Parallel()
	got, err := parseMCPEndpoints(" skills=http://skills:8090/mcp , audit=https://audit/mcp ")
	if err != nil || len(got) != 2 || got["skills"].Transport.URL != "http://skills:8090/mcp" || !got["audit"].Enabled {
		t.Fatalf("parse = %+v (%v)", got, err)
	}
	if got, err := parseMCPEndpoints(""); err != nil || len(got) != 0 {
		t.Fatalf("empty = %+v (%v)", got, err)
	}
	for _, bad := range []string{"noequals", "a=ftp://x", "=http://x", "a=http://x,a=http://y"} {
		if _, err := parseMCPEndpoints(bad); err == nil {
			t.Fatalf("%q must be rejected", bad)
		}
	}
}

func TestValidateServeConfig(t *testing.T) {
	t.Parallel()
	ok := appConfig{DBURL: "postgres://x", LLMURL: "http://llm/v1", LLMKey: "k", ConsoleURL: "http://c", SvcToken: "s", MaxConcurrentTurns: 8, DrainTimeout: time.Second}
	if err := validateServeConfig(ok); err != nil {
		t.Fatalf("valid config rejected: %v", err)
	}
	missing := ok
	missing.LLMKey = ""
	missing.SvcToken = ""
	err := validateServeConfig(missing)
	if err == nil || !strings.Contains(err.Error(), "AIRUSH_AGENT_LLM_KEY") || !strings.Contains(err.Error(), "AIRUSH_AGENT_SVC_TOKEN") {
		t.Fatalf("missing secrets: %v", err)
	}
	bad := ok
	bad.MaxConcurrentTurns = 0
	if validateServeConfig(bad) == nil {
		t.Fatal("zero concurrency accepted")
	}
	bad = ok
	bad.MCPEndpoints = "broken"
	if validateServeConfig(bad) == nil {
		t.Fatal("broken mcp endpoints accepted")
	}
	if podName() == "" {
		t.Fatal("pod name empty")
	}
}
