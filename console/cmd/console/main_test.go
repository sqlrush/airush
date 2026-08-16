package main

import (
	"regexp"
	"testing"

	"github.com/sqlrush/airush/libs/config"
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

// TestAppConfigAllFieldTypesLoadable：appConfig 的每个字段类型都必须是 libs/config 支持的。
// 类型不支持只在**运行期** Load 时报错——2026-08-15 一个 int64 字段让 kind 上的 migrate Job
// 与 console 全部起不来，而单测/lint/build 全绿。这条把它拉回测试期。
func TestAppConfigAllFieldTypesLoadable(t *testing.T) {
	// 只给必填 secret 塞占位值；其余走 default。目标是"不因字段类型报错"，不是校验业务值。
	for _, kv := range [][2]string{
		{"AIRUSH_CONSOLE_DB_URL", "postgres://x"},
		{"AIRUSH_CONSOLE_CREDENTIAL_KEK", "x"},
		{"AIRUSH_CONSOLE_SVC_TOKEN", "x"},
		{"AIRUSH_CONSOLE_CA_CERT", "x"},
		{"AIRUSH_CONSOLE_CA_KEY", "x"},
	} {
		t.Setenv(kv[0], kv[1])
	}
	if _, err := config.Load[appConfig](component); err != nil {
		t.Fatalf("appConfig 加载失败（字段类型或 tag 不被 libs/config 支持？）: %v", err)
	}
}
