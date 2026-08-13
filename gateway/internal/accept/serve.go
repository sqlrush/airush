package accept

import (
	"context"
	"crypto/tls"
	"crypto/x509"
	"errors"
	"fmt"
	"log/slog"
	"net"

	"google.golang.org/grpc"
	"google.golang.org/grpc/credentials"

	"github.com/sqlrush/airush/gateway/internal/consoleclient"
	"github.com/sqlrush/airush/libs/metrics"
	connectorv1 "github.com/sqlrush/airush/proto/gen/go/connector/v1"
)

// TLSMaterial 是 gateway 两个 gRPC 端口的证书料（部署侧 Secret 注入）。
// 注册端口用 server-TLS（客户端此时还没有证书）；会话端口用 mTLS（要求并验证客户端证书）。
type TLSMaterial struct {
	ServerCertPEM []byte // gateway 服务端证书（由内部 CA 签发，CN=gateway 服务名）
	ServerKeyPEM  []byte
	ClientCAPEM   []byte // 验证 connector 客户端证书的 CA（= 内部 CA bundle）
}

// Servers 持有两个 gRPC server 与共享的会话服务（供优雅退出 DrainAll）。
type Servers struct {
	enrollGRPC  *grpc.Server
	sessionGRPC *grpc.Server
	sessionSvc  *SessionServer
}

// Build 装配注册与会话两个 gRPC server（尚未监听）。
func Build(console *consoleclient.Client, tlsMat TLSMaterial, cfg SessionConfig, deps Deps) (*Servers, error) {
	serverCert, err := tls.X509KeyPair(tlsMat.ServerCertPEM, tlsMat.ServerKeyPEM)
	if err != nil {
		return nil, fmt.Errorf("accept: server keypair: %w", err)
	}
	clientCAs := x509.NewCertPool()
	if !clientCAs.AppendCertsFromPEM(tlsMat.ClientCAPEM) {
		return nil, errors.New("accept: append client CA failed")
	}

	enrollTLS := &tls.Config{Certificates: []tls.Certificate{serverCert}, MinVersion: tls.VersionTLS13}
	sessionTLS := &tls.Config{
		Certificates: []tls.Certificate{serverCert},
		ClientAuth:   tls.RequireAndVerifyClientCert,
		ClientCAs:    clientCAs,
		MinVersion:   tls.VersionTLS13,
	}

	enrollGRPC := grpc.NewServer(grpc.Creds(credentials.NewTLS(enrollTLS)))
	connectorv1.RegisterEnrollmentServiceServer(enrollGRPC, NewEnrollmentServer(console, deps.Logger))

	sessionSvc := NewSessionServer(console, cfg, deps.Sink, deps.SnapshotSink, deps.Logger)
	sessionGRPC := grpc.NewServer(grpc.Creds(credentials.NewTLS(sessionTLS)))
	connectorv1.RegisterSessionServiceServer(sessionGRPC, sessionSvc)

	return &Servers{enrollGRPC: enrollGRPC, sessionGRPC: sessionGRPC, sessionSvc: sessionSvc}, nil
}

// Deps 是接入面的横切依赖。
type Deps struct {
	Logger *slog.Logger
	Sink   metrics.Sink // Connector 指标上报落点（spec-1.3 §2.4）；nil 时丢弃
	// SnapshotSink 是慢日志/表结构/配置快照落点（spec-1.4）；nil 时丢弃。
	SnapshotSink metrics.SnapshotSink
}

// Serve 在给定监听器上并发启动两个 server（阻塞直到出错）。
func (s *Servers) Serve(enrollLn, sessionLn net.Listener) error {
	errCh := make(chan error, 2)
	go func() { errCh <- s.enrollGRPC.Serve(enrollLn) }()
	go func() { errCh <- s.sessionGRPC.Serve(sessionLn) }()
	return <-errCh
}

// GracefulStop 广播 Drain 后优雅停止两个 server。
func (s *Servers) GracefulStop(reason string) {
	s.sessionSvc.DrainAll(reason)
	s.enrollGRPC.GracefulStop()
	s.sessionGRPC.GracefulStop()
}

// Dispatch 向连接器下发指令并等终态（spec-1.3 平台驱动采集，经内部 collect API 调用）。
func (s *Servers) Dispatch(ctx context.Context, connectorID string, cmd *connectorv1.Command) error {
	return s.sessionSvc.Dispatch(ctx, connectorID, cmd)
}
