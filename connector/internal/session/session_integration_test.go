//go:build integration

// spec-1.2 D6 connector 侧：enroll + session 全流程对**假 gateway**（进程内 gRPC）。
// 假 gateway 用自签内部 CA 承载 mTLS，覆盖 enroll.Run 与 session.Client 主循环、
// 心跳、指令处理与 Drain 触发重连——不导入真 gateway/console（边界干净）。
package session_test

import (
	"context"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/tls"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/pem"
	"io"
	"log/slog"
	"math/big"
	"net"
	"os"
	"sync"
	"testing"
	"time"

	"google.golang.org/grpc"
	"google.golang.org/grpc/credentials"

	"github.com/sqlrush/airush/connector/internal/conf"
	"github.com/sqlrush/airush/connector/internal/enroll"
	"github.com/sqlrush/airush/connector/internal/session"
	connectorv1 "github.com/sqlrush/airush/proto/gen/go/connector/v1"
)

const (
	tenantID = "00000000-0000-0000-0000-000000000001"
	connID   = "11111111-1111-1111-1111-111111111111"
)

// fakeGateway 是进程内 gRPC gateway：签发证书 + 会话回声/指令下发/Drain。
type fakeGateway struct {
	connectorv1.UnimplementedEnrollmentServiceServer
	connectorv1.UnimplementedSessionServiceServer
	ca         *fakeCA
	grpcSrv    *grpc.Server
	addr       string
	caBundle   []byte
	sendCmd    chan *connectorv1.Command
	drainAfter int // 收到第 N 个心跳后发 Drain（0 = 不发）
	gotResults chan *connectorv1.CommandResult
	mu         sync.Mutex
	beats      int
	enrollErr  bool
}

func (g *fakeGateway) Enroll(_ context.Context, req *connectorv1.EnrollRequest) (*connectorv1.EnrollResponse, error) {
	g.mu.Lock()
	fail := g.enrollErr
	g.mu.Unlock()
	if fail {
		return nil, io.ErrUnexpectedEOF
	}
	certPEM := g.ca.sign(req.GetCsrPem())
	return &connectorv1.EnrollResponse{
		ConnectorId:    connID,
		CertificatePem: certPEM,
		CaBundlePem:    g.caBundle,
	}, nil
}

func (g *fakeGateway) Session(stream connectorv1.SessionService_SessionServer) error {
	first, err := stream.Recv()
	if err != nil {
		return err
	}
	if first.GetHello() == nil {
		return io.EOF
	}
	if err := stream.Send(&connectorv1.ServerFrame{Frame: &connectorv1.ServerFrame_HelloAck{
		HelloAck: &connectorv1.HelloAck{HeartbeatIntervalSeconds: 1},
	}}); err != nil {
		return err
	}
	for {
		f, err := stream.Recv()
		if err != nil {
			return nil
		}
		if f.GetHeartbeat() != nil {
			g.mu.Lock()
			g.beats++
			n := g.beats
			g.mu.Unlock()
			_ = stream.Send(&connectorv1.ServerFrame{Frame: &connectorv1.ServerFrame_HeartbeatAck{
				HeartbeatAck: &connectorv1.HeartbeatAck{Seq: f.GetHeartbeat().GetSeq()},
			}})
			select {
			case cmd := <-g.sendCmd:
				_ = stream.Send(&connectorv1.ServerFrame{Frame: &connectorv1.ServerFrame_Command{Command: cmd}})
			default:
			}
			if g.drainAfter > 0 && n >= g.drainAfter {
				_ = stream.Send(&connectorv1.ServerFrame{Frame: &connectorv1.ServerFrame_Drain{
					Drain: &connectorv1.Drain{Reason: "test drain"},
				}})
				return nil
			}
		}
		if r := f.GetCommandResult(); r != nil {
			select {
			case g.gotResults <- r:
			default:
			}
		}
	}
}

func newFakeGateway(t *testing.T) *fakeGateway {
	t.Helper()
	ca := newFakeCA(t)
	serverCert, serverKey := ca.issueServer(t)
	gwCert, err := tls.X509KeyPair(serverCert, serverKey)
	if err != nil {
		t.Fatalf("gw keypair: %v", err)
	}
	clientCAs := x509.NewCertPool()
	clientCAs.AppendCertsFromPEM(ca.certPEM)

	g := &fakeGateway{
		ca: ca, caBundle: ca.certPEM,
		sendCmd:    make(chan *connectorv1.Command, 1),
		gotResults: make(chan *connectorv1.CommandResult, 4),
	}
	// 注册端口用 server-TLS，会话端口用 mTLS——为简化，此 fake 单端口 mTLS 但
	// enroll 时客户端还没有证书，故 enroll 走无客户端证书的 server-TLS 端口。
	g.grpcSrv = grpc.NewServer(grpc.Creds(credentials.NewTLS(&tls.Config{
		Certificates: []tls.Certificate{gwCert},
		ClientAuth:   tls.VerifyClientCertIfGiven,
		ClientCAs:    clientCAs,
		MinVersion:   tls.VersionTLS13,
	})))
	connectorv1.RegisterEnrollmentServiceServer(g.grpcSrv, g)
	connectorv1.RegisterSessionServiceServer(g.grpcSrv, g)

	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("listen: %v", err)
	}
	g.addr = ln.Addr().String()
	go func() { _ = g.grpcSrv.Serve(ln) }()
	t.Cleanup(g.grpcSrv.Stop)
	return g
}

func (g *fakeGateway) creds() credentials.TransportCredentials {
	roots := x509.NewCertPool()
	roots.AppendCertsFromPEM(g.caBundle)
	return credentials.NewTLS(&tls.Config{RootCAs: roots, ServerName: "localhost", MinVersion: tls.VersionTLS13})
}

// TestEnrollThenSession 覆盖 enroll.Run + session.Client 主循环 + 指令处理 + Drain 重连。
func TestEnrollThenSession(t *testing.T) {
	g := newFakeGateway(t)
	store, err := conf.NewStore(t.TempDir())
	if err != nil {
		t.Fatalf("store: %v", err)
	}

	// enroll
	if err := enroll.Run(context.Background(), store, g.addr, "tok", "v1", g.creds()); err != nil {
		t.Fatalf("enroll: %v", err)
	}
	if !store.Enrolled() {
		t.Fatal("not enrolled after Run")
	}

	// session：下发一个 ECHO，Drain 在第 2 个心跳后触发（验证重连不 panic）
	g.sendCmd <- &connectorv1.Command{CommandId: "e1", Type: "ECHO", Payload: []byte("pong")}
	g.mu.Lock()
	g.drainAfter = 2
	g.mu.Unlock()

	certPEM, _ := store.ReadCert()
	keyPEM, _ := store.ReadKey()
	caPEM, _ := store.ReadCABundle()
	creds, err := session.MTLSCreds(certPEM, keyPEM, caPEM)
	if err != nil {
		t.Fatalf("mtls: %v", err)
	}
	id, _ := store.ReadConnectorID()
	client := session.New(session.Config{
		GatewayAddr: g.addr, ConnectorID: id, Version: "v1", MaxBackoff: 200 * time.Millisecond,
	}, creds, session.BuiltinHandler{}, slog.New(slog.NewTextHandler(io.Discard, nil)))

	ctx, cancel := context.WithTimeout(context.Background(), 4*time.Second)
	defer cancel()
	go func() { _ = client.Run(ctx) }()

	select {
	case r := <-g.gotResults:
		if string(r.GetPayload()) != "pong" {
			t.Fatalf("echo result = %q", r.GetPayload())
		}
	case <-time.After(3 * time.Second):
		t.Fatal("no command result received")
	}
}

// TestEnrollStoreWriteError 覆盖 enroll.Run 的 store 写入失败分支（只读目录）。
func TestEnrollStoreWriteError(t *testing.T) {
	g := newFakeGateway(t)
	dir := t.TempDir()
	store, err := conf.NewStore(dir)
	if err != nil {
		t.Fatalf("store: %v", err)
	}
	if err := os.Chmod(dir, 0o500); err != nil { //nolint:gosec // 测试：只读目录触发写失败
		t.Fatalf("chmod: %v", err)
	}
	t.Cleanup(func() { _ = os.Chmod(dir, 0o700) }) //nolint:gosec // 测试清理
	if err := enroll.Run(context.Background(), store, g.addr, "tok", "v1", g.creds()); err == nil {
		t.Fatal("enroll succeeded despite unwritable store")
	}
}

// TestEnrollRPCError 覆盖 enroll.Run 的 RPC 失败分支。
func TestEnrollRPCError(t *testing.T) {
	g := newFakeGateway(t)
	g.mu.Lock()
	g.enrollErr = true
	g.mu.Unlock()
	store, _ := conf.NewStore(t.TempDir())
	if err := enroll.Run(context.Background(), store, g.addr, "tok", "v1", g.creds()); err == nil {
		t.Fatal("enroll error not surfaced")
	}
	if store.Enrolled() {
		t.Fatal("store enrolled despite RPC error")
	}
}

// TestRunReconnectsThenCancels 覆盖 session.Run 的重连循环 + ctx 取消退出。
func TestRunReconnectsThenCancels(t *testing.T) {
	// 指向一个未监听地址 → runOnce 反复失败 → 退避；ctx 到期后 Run 返回。
	creds := credentials.NewTLS(&tls.Config{InsecureSkipVerify: true, MinVersion: tls.VersionTLS13}) //nolint:gosec // 测试
	client := session.New(session.Config{
		GatewayAddr: "127.0.0.1:1", ConnectorID: connID, Version: "v1", MaxBackoff: 50 * time.Millisecond,
	}, creds, session.BuiltinHandler{}, slog.New(slog.NewTextHandler(io.Discard, nil)))

	ctx, cancel := context.WithTimeout(context.Background(), 400*time.Millisecond)
	defer cancel()
	err := client.Run(ctx)
	if err == nil {
		t.Fatal("Run should return ctx error after cancel")
	}
}

// --- fake CA ---

type fakeCA struct {
	cert    *x509.Certificate
	key     *ecdsa.PrivateKey
	certPEM []byte
}

func newFakeCA(t *testing.T) *fakeCA {
	t.Helper()
	key, _ := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	tmpl := &x509.Certificate{
		SerialNumber: big.NewInt(1), Subject: pkix.Name{CommonName: "ca"},
		NotBefore: time.Now().Add(-time.Minute), NotAfter: time.Now().Add(time.Hour),
		IsCA: true, KeyUsage: x509.KeyUsageCertSign, BasicConstraintsValid: true,
	}
	der, _ := x509.CreateCertificate(rand.Reader, tmpl, tmpl, &key.PublicKey, key)
	cert, _ := x509.ParseCertificate(der)
	return &fakeCA{cert: cert, key: key, certPEM: pemCert(der)}
}

func (ca *fakeCA) sign(csrPEM []byte) []byte {
	block, _ := pem.Decode(csrPEM)
	csr, _ := x509.ParseCertificateRequest(block.Bytes)
	tmpl := &x509.Certificate{
		SerialNumber: big.NewInt(2), Subject: pkix.Name{CommonName: connID},
		NotBefore: time.Now().Add(-time.Minute), NotAfter: time.Now().Add(time.Hour),
		KeyUsage: x509.KeyUsageDigitalSignature, ExtKeyUsage: []x509.ExtKeyUsage{x509.ExtKeyUsageClientAuth},
	}
	der, _ := x509.CreateCertificate(rand.Reader, tmpl, ca.cert, csr.PublicKey, ca.key)
	return pemCert(der)
}

func (ca *fakeCA) issueServer(t *testing.T) (certPEM, keyPEM []byte) {
	t.Helper()
	key, _ := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	tmpl := &x509.Certificate{
		SerialNumber: big.NewInt(3), Subject: pkix.Name{CommonName: "gateway"},
		DNSNames:    []string{"localhost"},
		IPAddresses: []net.IP{net.ParseIP("127.0.0.1")}, // 客户端无 ServerName 时按 IP 目标验证
		NotBefore:   time.Now().Add(-time.Minute), NotAfter: time.Now().Add(time.Hour),
		KeyUsage: x509.KeyUsageDigitalSignature, ExtKeyUsage: []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth},
	}
	der, _ := x509.CreateCertificate(rand.Reader, tmpl, ca.cert, &key.PublicKey, ca.key)
	keyDER, _ := x509.MarshalECPrivateKey(key)
	return pemCert(der), pem.EncodeToMemory(&pem.Block{Type: "EC PRIVATE KEY", Bytes: keyDER})
}

func pemCert(der []byte) []byte {
	return pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: der})
}
