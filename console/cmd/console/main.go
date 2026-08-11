// Command console 是控制面入口：migrate 子命令（spec-0.6）+ 配置框架接入（spec-0.7）。
// API 服务随 spec-1.1 实装。
package main

import (
	"flag"
	"fmt"
	"os"
	"strings"

	"github.com/sqlrush/airush/console/internal/dbmigrate"
	"github.com/sqlrush/airush/libs/config"
)

const component = "console"

// appConfig 是 console 的全部配置面（.env.example 与此同步，CI 校验）。
type appConfig struct {
	LogLevel     string  `env:"LOG_LEVEL"          default:"info" oneof:"debug,info,warn,error" common:"true"`
	Listen       string  `env:"LISTEN_ADDR"        default:":8080"`
	OTLPEndpoint string  `env:"OTLP_ENDPOINT"      default:"" common:"true"`
	SampleRatio  float64 `env:"TRACE_SAMPLE_RATIO" default:"1.0" common:"true"`
	// DBURL / CredentialKEK 非启动必填（版本/横幅路径不需要）；migrate 与 --serve 分别显式校验。
	DBURL           string `env:"DB_URL"             secret:"true"`
	CredentialKEK   string `env:"CREDENTIAL_KEK"     secret:"true"`
	CredentialKEKID string `env:"CREDENTIAL_KEK_ID"  default:"v1"`
	DefaultTenantID string `env:"DEFAULT_TENANT_ID"  default:"00000000-0000-0000-0000-000000000001"`
}

// version 由构建期 -ldflags 注入（spec-0.10/0.11 定版链路）。
var version = "0.0.0-dev"

// banner 组装启动横幅（spec-0.4 D5 范本的被测单元）。
func banner(v string) string {
	return component + " " + v
}

func main() {
	printCfg := flag.Bool("print-config", false, "打印脱敏配置后退出")
	cfgKeys := flag.Bool("config-keys", false, "打印全部配置项变量名后退出")
	serve := flag.Bool("serve", false, "启动控制面 API 服务（spec-1.1）")
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
		serveMain(cfg)
		return
	}

	if args := flag.Args(); len(args) > 0 && args[0] == "migrate" {
		if cfg.DBURL == "" {
			fmt.Fprintln(os.Stderr, "error: AIRUSH_CONSOLE_DB_URL 未设置（migrate 需要控制面 PG 连接串）")
			os.Exit(2)
		}
		if err := dbmigrate.RunWithURL(cfg.DBURL, args[1:]); err != nil {
			fmt.Fprintln(os.Stderr, "error:", err)
			os.Exit(1)
		}
		return
	}
	fmt.Println(banner(version))
}
