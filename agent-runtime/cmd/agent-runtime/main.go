// Command agent-runtime 是智能体运行时（AD-11：codexgo 抽核宿主；spec-1.8）。
// 无状态：任何 pod 可接任何租户任何线程的 turn；会话状态全在控制面 PG（pgstore）。
package main

import (
	"context"
	"flag"
	"fmt"
	"os"
	"strings"
	"time"

	"github.com/sqlrush/airush/libs/config"
	"github.com/sqlrush/airush/libs/obs"
)

// component 是观测/日志里的组件名；配置前缀用 configComponent（AIRUSH_AGENT_*，spec-1.8 §2.6）。
const (
	component       = "agent-runtime"
	configComponent = "agent"
)

// appConfig 是 agent-runtime 的全部配置面（.env.example 与此同步，CI 校验；spec-1.8 §2.6）。
type appConfig struct {
	LogLevel     string  `env:"LOG_LEVEL"          default:"info" oneof:"debug,info,warn,error" common:"true"`
	Listen       string  `env:"LISTEN_ADDR"        default:":8082"`
	OTLPEndpoint string  `env:"OTLP_ENDPOINT"      default:"" common:"true"`
	SampleRatio  float64 `env:"TRACE_SAMPLE_RATIO" default:"1.0" common:"true"`

	// 控制面 PG（同库；租户事务 SET LOCAL ROLE airush_app）
	DBURL string `env:"DB_URL" secret:"true"`
	// LLM 网关（spec-1.7）：OpenAI 兼容根（…/v1）+ 网关 master key（Meter 注入 Authorization）
	LLMURL string `env:"LLM_URL" default:""`
	LLMKey string `env:"LLM_KEY" secret:"true"`
	// 控制面内部 API（配额门与记账，libs/llm.ConsoleClient）+ 服务间 token（也是本进程内部 API 的口令）
	ConsoleURL string `env:"CONSOLE_URL" default:""`
	SvcToken   string `env:"SVC_TOKEN"   secret:"true"`

	DefaultModel       string        `env:"DEFAULT_MODEL"        default:"chat-default"`
	MaxConcurrentTurns int           `env:"MAX_CONCURRENT_TURNS" default:"8"`
	DrainTimeout       time.Duration `env:"DRAIN_TIMEOUT"        default:"300s"`
	// 静态 skill MCP endpoints：逗号分隔的 name=url（streamable HTTP）；1.9 换注册表
	MCPEndpoints    string `env:"MCP_ENDPOINTS"     default:""`
	DefaultTenantID string `env:"DEFAULT_TENANT_ID" default:"00000000-0000-0000-0000-000000000001"`
}

// version 由构建期 -ldflags 注入（spec-0.10/0.11 定版链路）。
var version = "0.0.0-dev"

func main() {
	printCfg := flag.Bool("print-config", false, "打印脱敏配置后退出")
	cfgKeys := flag.Bool("config-keys", false, "打印全部配置项变量名后退出")
	serve := flag.Bool("serve", false, "启动运行时服务（内部 HTTP + SSE）")
	flag.Bool("version", false, "print version and exit")
	flag.Parse()

	if *cfgKeys {
		fmt.Println(strings.Join(config.Keys[appConfig](configComponent), "\n"))
		return
	}
	cfg, err := config.Load[appConfig](configComponent)
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(2)
	}
	if *printCfg {
		fmt.Println(config.Redacted(configComponent, cfg))
		return
	}
	if *serve {
		if err := validateServeConfig(cfg); err != nil {
			fmt.Fprintln(os.Stderr, "config:", err)
			os.Exit(2)
		}
		provider := obs.Init(context.Background(), obs.Config{
			Component:    component,
			OTLPEndpoint: cfg.OTLPEndpoint,
			SampleRatio:  cfg.SampleRatio,
			LogLevel:     cfg.LogLevel,
		})
		if err := runServer(cfg, provider, version); err != nil {
			fmt.Fprintln(os.Stderr, "error:", err)
			os.Exit(1)
		}
		return
	}
	fmt.Printf("%s %s\n", component, version)
}

// validateServeConfig 启动前校验必需项存在（secrets 走环境变量，缺失即拒起——规则 5/安全原则 5）。
func validateServeConfig(cfg appConfig) error {
	missing := []string{}
	if cfg.DBURL == "" {
		missing = append(missing, "AIRUSH_AGENT_DB_URL")
	}
	if cfg.LLMURL == "" {
		missing = append(missing, "AIRUSH_AGENT_LLM_URL")
	}
	if cfg.LLMKey == "" {
		missing = append(missing, "AIRUSH_AGENT_LLM_KEY")
	}
	if cfg.ConsoleURL == "" {
		missing = append(missing, "AIRUSH_AGENT_CONSOLE_URL")
	}
	if cfg.SvcToken == "" {
		missing = append(missing, "AIRUSH_AGENT_SVC_TOKEN")
	}
	if len(missing) > 0 {
		return fmt.Errorf("缺少必需配置: %s", strings.Join(missing, ", "))
	}
	if cfg.MaxConcurrentTurns <= 0 {
		return fmt.Errorf("AIRUSH_AGENT_MAX_CONCURRENT_TURNS 必须 > 0")
	}
	if cfg.DrainTimeout <= 0 {
		return fmt.Errorf("AIRUSH_AGENT_DRAIN_TIMEOUT 必须 > 0")
	}
	if _, err := parseMCPEndpoints(cfg.MCPEndpoints); err != nil {
		return err
	}
	return nil
}
