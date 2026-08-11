// Command connector 是客户侧接入代理（spec-1.2 D5）：outbound-only mTLS 长连接。
// 两个子命令：--enroll（一次性注册换证书）、--run（维持会话）。
package main

import (
	"context"
	"crypto/tls"
	"crypto/x509"
	"flag"
	"fmt"
	"os"
	"os/signal"
	"strings"
	"syscall"

	"google.golang.org/grpc/credentials"

	"github.com/sqlrush/airush/connector/internal/conf"
	"github.com/sqlrush/airush/connector/internal/enroll"
	"github.com/sqlrush/airush/connector/internal/session"
	"github.com/sqlrush/airush/libs/config"
	"github.com/sqlrush/airush/libs/obs"
)

const component = "connector"

// appConfig 是 connector 的全部配置面（.env.example 与此同步，CI 校验）。
// 注册面（server-TLS）与会话面（mTLS）在网关不同端口，故两个地址。
type appConfig struct {
	LogLevel    string `env:"LOG_LEVEL"     default:"info" oneof:"debug,info,warn,error" common:"true"`
	ConfigDir   string `env:"CONFIG_DIR"    default:""`
	EnrollAddr  string `env:"ENROLL_ADDR"   default:""`
	SessionAddr string `env:"SESSION_ADDR"  default:""`
	// EnrollToken 仅 --enroll 时需要（secret）。
	EnrollToken string `env:"ENROLL_TOKEN" secret:"true"`
	// EnrollCAPEM：注册端 server-TLS 的信任根（PEM 内容或空=用系统根）。
	EnrollCAPEM string `env:"ENROLL_CA_PEM"`
}

// version 由构建期 -ldflags 注入。
var version = "0.0.0-dev"

func main() {
	printCfg := flag.Bool("print-config", false, "打印脱敏配置后退出")
	cfgKeys := flag.Bool("config-keys", false, "打印全部配置项变量名后退出")
	doEnroll := flag.Bool("enroll", false, "用一次性令牌注册并退出")
	doRun := flag.Bool("run", false, "维持会话（长驻）")
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

	switch {
	case *doEnroll:
		mustEnroll(cfg)
	case *doRun:
		mustRun(cfg)
	default:
		fmt.Printf("connector %s\n", version)
	}
}

func mustEnroll(cfg appConfig) {
	requireAddr(cfg.EnrollAddr, "AIRUSH_CONNECTOR_ENROLL_ADDR")
	if cfg.EnrollToken == "" {
		fatal("AIRUSH_CONNECTOR_ENROLL_TOKEN 未设置（--enroll 必需）")
	}
	store, err := conf.NewStore(cfg.ConfigDir)
	if err != nil {
		fatal(err.Error())
	}
	creds, err := enrollCreds(cfg.EnrollCAPEM)
	if err != nil {
		fatal(err.Error())
	}
	if err := enroll.Run(context.Background(), store, cfg.EnrollAddr, cfg.EnrollToken, version, creds); err != nil {
		fatal(err.Error())
	}
	fmt.Println("enroll ok")
}

func mustRun(cfg appConfig) {
	requireAddr(cfg.SessionAddr, "AIRUSH_CONNECTOR_SESSION_ADDR")
	store, err := conf.NewStore(cfg.ConfigDir)
	if err != nil {
		fatal(err.Error())
	}
	if !store.Enrolled() {
		fatal("尚未注册，请先执行 connector --enroll")
	}
	provider := obs.Init(context.Background(), obs.Config{Component: component, LogLevel: cfg.LogLevel})

	certPEM, _ := store.ReadCert()
	keyPEM, _ := store.ReadKey()
	caPEM, _ := store.ReadCABundle()
	connectorID, _ := store.ReadConnectorID()
	creds, err := session.MTLSCreds(certPEM, keyPEM, caPEM)
	if err != nil {
		fatal(err.Error())
	}

	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer stop()
	client := session.New(session.Config{
		GatewayAddr: cfg.SessionAddr, ConnectorID: connectorID, Version: version,
	}, creds, session.BuiltinHandler{}, provider.Logger)
	if err := client.Run(ctx); err != nil && ctx.Err() == nil {
		fatal(err.Error())
	}
	provider.Logger.Info("connector stopped")
}

// enrollCreds 注册端 server-TLS 凭据（有 CA PEM 则钉，否则系统根）。
func enrollCreds(caPEM string) (credentials.TransportCredentials, error) {
	if caPEM == "" {
		return credentials.NewTLS(&tls.Config{MinVersion: tls.VersionTLS13}), nil
	}
	roots := x509.NewCertPool()
	if !roots.AppendCertsFromPEM([]byte(caPEM)) {
		return nil, fmt.Errorf("connector: append enroll CA failed")
	}
	return credentials.NewTLS(&tls.Config{RootCAs: roots, MinVersion: tls.VersionTLS13}), nil
}

func requireAddr(addr, name string) {
	if addr == "" {
		fatal(name + " 未设置")
	}
}

func fatal(msg string) {
	fmt.Fprintln(os.Stderr, "error:", msg)
	os.Exit(1)
}
