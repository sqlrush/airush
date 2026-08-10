package config

import (
	"os"
	"strings"
	"testing"
	"time"
)

type testCfg struct {
	Listen   string        `env:"LISTEN_ADDR" default:":8080"`
	LogLevel string        `env:"LOG_LEVEL"   default:"info" oneof:"debug,info,warn,error" common:"true"`
	DBURL    string        `env:"DB_URL"      required:"true" secret:"true"`
	Timeout  time.Duration `env:"TIMEOUT"     default:"30s"`
	Workers  int           `env:"WORKERS"     default:"4"`
	Ratio    float64       `env:"RATIO"       default:"1.0"`
}

// T1：全量合法加载。
func TestLoadValid(t *testing.T) {
	t.Setenv("AIRUSH_TESTAPP_DB_URL", "postgres://u:p@h/db")
	t.Setenv("AIRUSH_TESTAPP_WORKERS", "8")

	cfg, err := Load[testCfg]("testapp")
	if err != nil {
		t.Fatalf("load: %v", err)
	}
	if cfg.Listen != ":8080" || cfg.LogLevel != "info" || cfg.Workers != 8 || cfg.Timeout != 30*time.Second || cfg.Ratio != 1.0 {
		t.Fatalf("unexpected cfg: %+v", cfg)
	}
}

// T2：缺必填 + 非法枚举 + 非法整数 → 一次聚合报告 3 项。
func TestLoadAggregatesErrors(t *testing.T) {
	t.Setenv("AIRUSH_TESTAPP_LOG_LEVEL", "loud")
	t.Setenv("AIRUSH_TESTAPP_WORKERS", "many")

	_, err := Load[testCfg]("testapp")
	if err == nil {
		t.Fatal("expected error")
	}
	le, ok := err.(*LoadError)
	if !ok {
		t.Fatalf("expected *LoadError, got %T", err)
	}
	if len(le.Fields) != 3 {
		t.Fatalf("expected 3 field errors, got %d: %v", len(le.Fields), le.Fields)
	}
}

// T3：Redacted 输出 secret 为 ***。
func TestRedactedMasksSecret(t *testing.T) {
	t.Setenv("AIRUSH_TESTAPP_DB_URL", "postgres://user:supersecret@h/db")
	cfg, err := Load[testCfg]("testapp")
	if err != nil {
		t.Fatalf("load: %v", err)
	}
	out := Redacted("testapp", cfg)
	if strings.Contains(out, "supersecret") {
		t.Fatalf("redacted output leaks secret: %s", out)
	}
	if !strings.Contains(out, "AIRUSH_TESTAPP_DB_URL=***") {
		t.Fatalf("secret not masked: %s", out)
	}
}

// T4：secret 字段解析失败的错误信息不含原始值。
func TestSecretErrorHidesValue(t *testing.T) {
	type c struct {
		Port int `env:"PORT" required:"true" secret:"true"`
	}
	t.Setenv("AIRUSH_TESTAPP_PORT", "leak-me-not")
	_, err := Load[c]("testapp")
	if err == nil {
		t.Fatal("expected error")
	}
	if strings.Contains(err.Error(), "leak-me-not") {
		t.Fatalf("error leaks secret value: %v", err)
	}
}

// T5：组件级覆盖 COMMON 回退。
func TestCommonFallbackAndOverride(t *testing.T) {
	t.Setenv("AIRUSH_TESTAPP_DB_URL", "x")
	t.Setenv("AIRUSH_COMMON_LOG_LEVEL", "warn")

	cfg, err := Load[testCfg]("testapp")
	if err != nil {
		t.Fatalf("load: %v", err)
	}
	if cfg.LogLevel != "warn" {
		t.Fatalf("common fallback failed: %q", cfg.LogLevel)
	}

	t.Setenv("AIRUSH_TESTAPP_LOG_LEVEL", "error")
	cfg, err = Load[testCfg]("testapp")
	if err != nil {
		t.Fatalf("load: %v", err)
	}
	if cfg.LogLevel != "error" {
		t.Fatalf("component override failed: %q", cfg.LogLevel)
	}
}

// T6：production 不加载 .env（用当前目录 .env 探测）。
func TestProductionIgnoresDotenv(t *testing.T) {
	dir := t.TempDir()
	t.Chdir(dir)
	writeFile(t, dir+"/.env", "AIRUSH_TESTAPP_DB_URL=from-dotenv\n")

	t.Setenv("AIRUSH_ENV", "production")
	_, err := Load[testCfg]("testapp")
	if err == nil {
		t.Fatal("production should not read .env, DB_URL must be missing")
	}

	t.Setenv("AIRUSH_ENV", "dev")
	cfg, err := Load[testCfg]("testapp")
	if err != nil {
		t.Fatalf("dev load: %v", err)
	}
	if cfg.DBURL != "from-dotenv" {
		t.Fatalf("dev should read .env, got %q", cfg.DBURL)
	}
}

// T7：显式 env 优先于 .env。
func TestEnvBeatsDotenv(t *testing.T) {
	dir := t.TempDir()
	t.Chdir(dir)
	writeFile(t, dir+"/.env", "AIRUSH_TESTAPP_DB_URL=from-dotenv\n")
	t.Setenv("AIRUSH_TESTAPP_DB_URL", "from-env")

	cfg, err := Load[testCfg]("testapp")
	if err != nil {
		t.Fatalf("load: %v", err)
	}
	if cfg.DBURL != "from-env" {
		t.Fatalf("env should win over .env, got %q", cfg.DBURL)
	}
}

// Keys 覆盖（T8 脚本的库侧依赖）。
func TestKeysListsAllEnvNames(t *testing.T) {
	t.Parallel()
	keys := Keys[testCfg]("testapp")
	want := []string{
		"AIRUSH_TESTAPP_DB_URL", "AIRUSH_TESTAPP_LISTEN_ADDR",
		"AIRUSH_TESTAPP_LOG_LEVEL", "AIRUSH_TESTAPP_RATIO",
		"AIRUSH_TESTAPP_TIMEOUT", "AIRUSH_TESTAPP_WORKERS",
	}
	if len(keys) != len(want) {
		t.Fatalf("keys = %v, want %v", keys, want)
	}
	for i := range want {
		if keys[i] != want[i] {
			t.Fatalf("keys[%d] = %s, want %s", i, keys[i], want[i])
		}
	}
}

func writeFile(t *testing.T, path, content string) {
	t.Helper()
	if err := os.WriteFile(path, []byte(content), 0o600); err != nil {
		t.Fatalf("write %s: %v", path, err)
	}
}
