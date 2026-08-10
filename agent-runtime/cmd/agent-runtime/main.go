// Command agent-runtime is a scaffold placeholder（spec-0.1）+ 配置框架接入（spec-0.7 D5）；
// 真实实现从对应 Stage 1 spec 起整体替换本文件。
package main

import (
	"flag"
	"fmt"
	"os"
	"strings"

	"github.com/sqlrush/airush/libs/config"
)

const component = "agentruntime"

// appConfig 是 agent-runtime 的全部配置面（.env.example 与此同步，CI 校验）。
type appConfig struct {
	LogLevel string `env:"LOG_LEVEL" default:"info" oneof:"debug,info,warn,error" common:"true"`
	Listen   string `env:"LISTEN_ADDR" default:":8082"`
}

// version 由构建期 -ldflags 注入（spec-0.10/0.11 定版链路）。
var version = "0.0.0-dev"

func main() {
	printCfg := flag.Bool("print-config", false, "打印脱敏配置后退出")
	cfgKeys := flag.Bool("config-keys", false, "打印全部配置项变量名后退出")
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
	fmt.Printf("agent-runtime %s\n", version)
	_ = cfg
}
