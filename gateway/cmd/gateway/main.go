// Command gateway 是 Connector 接入网关。当前提供观测演示端点（spec-0.9 D5，
// 兼 Stage 0 验收 hello-world 载体）；隧道能力随 spec-1.2 实装。
package main

import (
	"context"
	"flag"
	"fmt"
	"os"
	"strings"

	"github.com/sqlrush/airush/libs/config"
	"github.com/sqlrush/airush/libs/obs"
)

const component = "gateway"

// appConfig 是 gateway 的全部配置面（.env.example 与此同步，CI 校验）。
type appConfig struct {
	LogLevel     string  `env:"LOG_LEVEL"          default:"info" oneof:"debug,info,warn,error" common:"true"`
	Listen       string  `env:"LISTEN_ADDR"        default:":8081"`
	OTLPEndpoint string  `env:"OTLP_ENDPOINT"      default:"" common:"true"`
	SampleRatio  float64 `env:"TRACE_SAMPLE_RATIO" default:"1.0" common:"true"`
	// spec-1.2 接入面：注册（server-TLS）与会话（mTLS）两个 gRPC 端口 + console 内部 API。
	EnrollListen  string `env:"ENROLL_LISTEN"   default:":8082"`
	SessionListen string `env:"SESSION_LISTEN"  default:":8083"`
	ConsoleURL    string `env:"CONSOLE_URL"     default:""`
	SvcToken      string `env:"SVC_TOKEN"       secret:"true"`
	TLSCertPEM    string `env:"TLS_CERT_PEM"    secret:"true"`
	TLSKeyPEM     string `env:"TLS_KEY_PEM"     secret:"true"`
	ClientCAPEM   string `env:"CLIENT_CA_PEM"   secret:"true"`
}

// version 由构建期 -ldflags 注入（spec-0.10/0.11 定版链路）。
var version = "0.0.0-dev"

func main() {
	printCfg := flag.Bool("print-config", false, "打印脱敏配置后退出")
	cfgKeys := flag.Bool("config-keys", false, "打印全部配置项变量名后退出")
	serve := flag.Bool("serve", false, "启动 HTTP 服务（healthz/demo 观测端点）")
	flag.Bool("version", false, "print version and exit")
	flag.Parse()

	if *cfgKeys {
		fmt.Println(strings.Join(config.Keys[appConfig](component), "\n"))
		return
	}
	cfg, err := config.Load[appConfig](component)
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(2)
	}
	if *printCfg {
		fmt.Println(config.Redacted(component, cfg))
		return
	}
	if *serve {
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
