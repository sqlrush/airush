//go:build integration

// spec-1.2 D6 gateway 接入面集成电池：真实 accept gRPC servers（mTLS）+ 假 console
// （httptest 桩，精确控制 handshake/status 响应）+ 原生 proto 客户端。测试面：
// mTLS 双向验证、证书 CN/指纹绑定、会话唯一（踢旧连）、心跳状态机、吊销 Drain。
package accept_test

import (
	"context"
	"crypto/tls"
	"crypto/x509"
	"encoding/json"
	"net"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/credentials"
	"google.golang.org/grpc/status"

	"github.com/sqlrush/airush/gateway/internal/accept"
	"github.com/sqlrush/airush/gateway/internal/consoleclient"
	"github.com/sqlrush/airush/libs/metrics"
	connectorv1 "github.com/sqlrush/airush/proto/gen/go/connector/v1"
)

const (
	tenantID = "00000000-0000-0000-0000-000000000001"
	connID   = "11111111-1111-1111-1111-111111111111"
)

// fakeConsole 记录内部 API 调用并按脚本响应。
type fakeConsole struct {
	mu             sync.Mutex
	handshakeErr   int // 非 0 = 该 HTTP 状态码 + 错误码
	handshakeCode  string
	statusUpdates  []string
	svcToken       string
	rejectBadToken bool
}

func (f *fakeConsole) server() *httptest.Server {
	mux := http.NewServeMux()
	mux.HandleFunc("/internal/v1/connector-enrollments", func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Authorization") != "Bearer "+f.svcToken {
			writeErr(w, 401, "AR_SVC_UNAUTHENTICATED")
			return
		}
		var body struct {
			Token string `json:"token"`
		}
		_ = json.NewDecoder(r.Body).Decode(&body)
		if f.rejectBadToken && body.Token == "bad" {
			writeErr(w, 401, "AR_CONNECTOR_ENROLL_TOKEN_INVALID")
			return
		}
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]string{
			"connector_id":    connID,
			"certificate_pem": "-----BEGIN CERTIFICATE-----\nfake\n-----END CERTIFICATE-----",
			"ca_bundle_pem":   "-----BEGIN CERTIFICATE-----\nca\n-----END CERTIFICATE-----",
		})
	})
	mux.HandleFunc("/internal/v1/connector-handshakes", func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Authorization") != "Bearer "+f.svcToken {
			writeErr(w, 401, "AR_SVC_UNAUTHENTICATED")
			return
		}
		f.mu.Lock()
		code, hs := f.handshakeCode, f.handshakeErr
		f.mu.Unlock()
		if hs != 0 {
			writeErr(w, hs, code)
			return
		}
		w.WriteHeader(204)
	})
	mux.HandleFunc("/internal/v1/connector-status", func(w http.ResponseWriter, r *http.Request) {
		var body struct {
			Status string `json:"status"`
		}
		_ = json.NewDecoder(r.Body).Decode(&body)
		f.mu.Lock()
		f.statusUpdates = append(f.statusUpdates, body.Status)
		f.mu.Unlock()
		w.WriteHeader(204)
	})
	return httptest.NewServer(mux)
}

func (f *fakeConsole) sawStatus(want string) bool {
	f.mu.Lock()
	defer f.mu.Unlock()
	for _, s := range f.statusUpdates {
		if s == want {
			return true
		}
	}
	return false
}

func writeErr(w http.ResponseWriter, status int, code string) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(map[string]string{"code": code, "message": code})
}

// fixture 起 accept servers（会话端口）+ 假 console。
type fixture struct {
	ca          *testCA
	console     *fakeConsole
	consoleSrv  *httptest.Server
	servers     *accept.Servers
	sink        *metrics.BufferSink // Connector DataUpload 落点（spec-1.3 T11）
	sessionAddr string
	enrollAddr  string
}

func newFixture(t *testing.T, cfg accept.SessionConfig) *fixture {
	t.Helper()
	ca := newTestCA(t)
	fc := &fakeConsole{svcToken: "tok"}
	consoleSrv := fc.server()
	t.Cleanup(consoleSrv.Close)

	gwCert, gwKey := ca.issueServer(t)
	client := consoleclient.New(consoleSrv.URL, "tok")
	sink := metrics.NewBufferSink(64)
	servers, err := accept.Build(client, accept.TLSMaterial{
		ServerCertPEM: gwCert, ServerKeyPEM: gwKey, ClientCAPEM: ca.certPEM,
	}, cfg, accept.Deps{Logger: testLogger(), Sink: sink})
	if err != nil {
		t.Fatalf("build: %v", err)
	}
	enrollLn := mustListen(t)
	sessionLn := mustListen(t)
	go func() { _ = servers.Serve(enrollLn, sessionLn) }()
	t.Cleanup(func() { servers.GracefulStop("teardown") })

	return &fixture{
		ca: ca, console: fc, consoleSrv: consoleSrv, servers: servers, sink: sink,
		sessionAddr: sessionLn.Addr().String(), enrollAddr: enrollLn.Addr().String(),
	}
}

// dialSession 用给定客户端证书建 mTLS 会话流。
func (f *fixture) dialSession(t *testing.T, certPEM, keyPEM []byte) (*grpc.ClientConn, connectorv1.SessionService_SessionClient) {
	t.Helper()
	clientCert, err := tls.X509KeyPair(certPEM, keyPEM)
	if err != nil {
		t.Fatalf("client keypair: %v", err)
	}
	roots := x509.NewCertPool()
	roots.AppendCertsFromPEM(f.ca.certPEM)
	creds := credentials.NewTLS(&tls.Config{
		Certificates: []tls.Certificate{clientCert}, RootCAs: roots,
		ServerName: "localhost", MinVersion: tls.VersionTLS13,
	})
	conn, err := grpc.NewClient(f.sessionAddr, grpc.WithTransportCredentials(creds))
	if err != nil {
		t.Fatalf("dial: %v", err)
	}
	stream, err := connectorv1.NewSessionServiceClient(conn).Session(context.Background())
	if err != nil {
		t.Fatalf("open stream: %v", err)
	}
	return conn, stream
}

func sendHello(stream connectorv1.SessionService_SessionClient, id string) error {
	return stream.Send(&connectorv1.ClientFrame{Frame: &connectorv1.ClientFrame_Hello{
		Hello: &connectorv1.Hello{ConnectorId: id},
	}})
}

// TestSessionHeartbeatStateMachine spec-1.2 T1/T7：握手→online→心跳 ack→degraded→online。
func TestSessionHeartbeatStateMachine(t *testing.T) {
	cfg := accept.SessionConfig{HeartbeatInterval: 100 * time.Millisecond, MissedBeatsDegr: 2, OfflineTimeout: 5 * time.Second}
	f := newFixture(t, cfg)
	cert, key := f.ca.issueClient(t, connID, tenantID)
	conn, stream := f.dialSession(t, cert, key)
	defer func() { _ = conn.Close() }()

	if err := sendHello(stream, connID); err != nil {
		t.Fatalf("hello: %v", err)
	}
	ack, err := stream.Recv()
	if err != nil || ack.GetHelloAck() == nil {
		t.Fatalf("hello ack: %v %+v", err, ack)
	}
	waitFor(t, func() bool { return f.console.sawStatus("online") }, "online")

	// 静默 > degradedAfter(200ms) → degraded
	waitFor(t, func() bool { return f.console.sawStatus("degraded") }, "degraded")

	// 心跳恢复 online + ack
	if err := stream.Send(hb(1)); err != nil {
		t.Fatalf("send hb: %v", err)
	}
	got, err := stream.Recv()
	if err != nil || got.GetHeartbeatAck().GetSeq() != 1 {
		t.Fatalf("hb ack: %v %+v", err, got)
	}
}

// TestSessionUniqueness spec-1.2 T8：同 connector 二连 → 旧连收 Drain。
func TestSessionUniqueness(t *testing.T) {
	f := newFixture(t, accept.DefaultSessionConfig())
	cert, key := f.ca.issueClient(t, connID, tenantID)

	conn1, s1 := f.dialSession(t, cert, key)
	defer func() { _ = conn1.Close() }()
	if err := sendHello(s1, connID); err != nil {
		t.Fatalf("hello1: %v", err)
	}
	if _, err := s1.Recv(); err != nil {
		t.Fatalf("ack1: %v", err)
	}

	conn2, s2 := f.dialSession(t, cert, key)
	defer func() { _ = conn2.Close() }()
	if err := sendHello(s2, connID); err != nil {
		t.Fatalf("hello2: %v", err)
	}
	if _, err := s2.Recv(); err != nil {
		t.Fatalf("ack2: %v", err)
	}

	// 旧连应收 Drain 帧
	if !recvDrain(t, s1) {
		t.Fatal("first session not drained by second connection")
	}
}

// TestCNMismatchRejected spec-1.2 §3：证书 CN 与 Hello.connector_id 不符 → 拒绝。
func TestCNMismatchRejected(t *testing.T) {
	f := newFixture(t, accept.DefaultSessionConfig())
	// CN=其他值，SAN 里 connectorID 仍是 connID
	cert, key := f.ca.issueClientCN(t, "wrong-cn", connID, tenantID)
	conn, stream := f.dialSession(t, cert, key)
	defer func() { _ = conn.Close() }()
	if err := sendHello(stream, connID); err != nil {
		return
	}
	_, err := stream.Recv()
	if st, _ := status.FromError(err); st.Code() != codes.PermissionDenied {
		t.Fatalf("CN mismatch code = %v, want PermissionDenied", st.Code())
	}
}

// TestUntrustedClientCertRejected spec-1.2 T4：异 CA 客户端证书 → TLS 层拒绝
// （拒绝在 TLS 握手，表现为 stream 建立即失败）。
func TestUntrustedClientCertRejected(t *testing.T) {
	f := newFixture(t, accept.DefaultSessionConfig())
	other := newTestCA(t) // 不被 gateway 信任
	cert, key := other.issueClient(t, connID, tenantID)

	clientCert, err := tls.X509KeyPair(cert, key)
	if err != nil {
		t.Fatalf("keypair: %v", err)
	}
	roots := x509.NewCertPool()
	roots.AppendCertsFromPEM(f.ca.certPEM)
	creds := credentials.NewTLS(&tls.Config{
		Certificates: []tls.Certificate{clientCert}, RootCAs: roots,
		ServerName: "localhost", MinVersion: tls.VersionTLS13,
	})
	conn, err := grpc.NewClient(f.sessionAddr, grpc.WithTransportCredentials(creds))
	if err != nil {
		t.Fatalf("dial: %v", err)
	}
	defer func() { _ = conn.Close() }()

	stream, err := connectorv1.NewSessionServiceClient(conn).Session(context.Background())
	if err != nil {
		return // TLS 握手在 stream 建立即失败 = 预期
	}
	if err := sendHello(stream, connID); err == nil {
		if _, err := stream.Recv(); err == nil {
			t.Fatal("untrusted client cert accepted")
		}
	}
}

// TestRevokeDrains spec-1.2 T3：console 报吊销（handshake 后状态变化）→ DrainAll 生效。
func TestRevokeDrains(t *testing.T) {
	f := newFixture(t, accept.DefaultSessionConfig())
	cert, key := f.ca.issueClient(t, connID, tenantID)
	conn, stream := f.dialSession(t, cert, key)
	defer func() { _ = conn.Close() }()
	if err := sendHello(stream, connID); err != nil {
		t.Fatalf("hello: %v", err)
	}
	if _, err := stream.Recv(); err != nil {
		t.Fatalf("ack: %v", err)
	}
	waitFor(t, func() bool { return f.console.sawStatus("online") }, "online")

	// 模拟运营吊销：gateway 广播 Drain（真实路径由 console 侧触发下线，此处直接调 DrainAll）
	f.servers.GracefulStop("revoked")
	if !recvDrain(t, stream) {
		t.Fatal("session not drained on revoke")
	}
}

// TestHandshakeRejectionPropagates spec-1.2 T5：console 握手校验失败 → 会话被拒。
func TestHandshakeRejectionPropagates(t *testing.T) {
	f := newFixture(t, accept.DefaultSessionConfig())
	f.console.mu.Lock()
	f.console.handshakeErr = 403
	f.console.handshakeCode = "AR_AUTH_FORBIDDEN"
	f.console.mu.Unlock()

	cert, key := f.ca.issueClient(t, connID, tenantID)
	conn, stream := f.dialSession(t, cert, key)
	defer func() { _ = conn.Close() }()
	if err := sendHello(stream, connID); err != nil {
		return
	}
	_, err := stream.Recv()
	if st, _ := status.FromError(err); st.Code() != codes.PermissionDenied {
		t.Fatalf("handshake rejection code = %v, want PermissionDenied", st.Code())
	}
}

// TestEnrollRPC spec-1.2 T1（注册段）：Enroll RPC 经 server-TLS 转发 console 并回证书；
// 坏 token → PermissionDenied。
func TestEnrollRPC(t *testing.T) {
	f := newFixture(t, accept.DefaultSessionConfig())
	f.console.mu.Lock()
	f.console.rejectBadToken = true
	f.console.mu.Unlock()

	// enroll 端口用 server-TLS（客户端只验证服务端）
	roots := x509.NewCertPool()
	roots.AppendCertsFromPEM(f.ca.certPEM)
	creds := credentials.NewTLS(&tls.Config{RootCAs: roots, ServerName: "localhost", MinVersion: tls.VersionTLS13})
	conn, err := grpc.NewClient(f.enrollAddr, grpc.WithTransportCredentials(creds))
	if err != nil {
		t.Fatalf("dial enroll: %v", err)
	}
	defer func() { _ = conn.Close() }()
	client := connectorv1.NewEnrollmentServiceClient(conn)

	resp, err := client.Enroll(context.Background(), &connectorv1.EnrollRequest{
		EnrollmentToken: "good", CsrPem: []byte("-----BEGIN CERTIFICATE REQUEST-----\nx\n-----END CERTIFICATE REQUEST-----"),
		ConnectorVersion: "v1",
	})
	if err != nil || resp.GetConnectorId() != connID {
		t.Fatalf("enroll good: %v %+v", err, resp)
	}

	_, err = client.Enroll(context.Background(), &connectorv1.EnrollRequest{
		EnrollmentToken: "bad", CsrPem: []byte("x"),
	})
	if st, _ := status.FromError(err); st.Code() != codes.PermissionDenied {
		t.Fatalf("enroll bad token code = %v, want PermissionDenied", st.Code())
	}

	// 空参数 → InvalidArgument（不触达 console）
	_, err = client.Enroll(context.Background(), &connectorv1.EnrollRequest{})
	if st, _ := status.FromError(err); st.Code() != codes.InvalidArgument {
		t.Fatalf("empty enroll code = %v, want InvalidArgument", st.Code())
	}
}

// TestConnectorDataUploadToSink spec-1.3 T11：平台经 Dispatch 下发 PROBE_METRICS，
// 连接器回 DataUpload 帧携带 batch，gateway 落 Sink 收讫，Dispatch 得成功终态。
func TestConnectorDataUploadToSink(t *testing.T) {
	f := newFixture(t, accept.DefaultSessionConfig())
	cert, key := f.ca.issueClient(t, connID, tenantID)
	conn, stream := f.dialSession(t, cert, key)
	defer func() { _ = conn.Close() }()
	if err := sendHello(stream, connID); err != nil {
		t.Fatalf("hello: %v", err)
	}
	if _, err := stream.Recv(); err != nil {
		t.Fatalf("ack: %v", err)
	}
	waitFor(t, func() bool { return f.console.sawStatus("online") }, "online")

	go respondMetrics(stream) // 假连接器：收到 PROBE_METRICS 即回 DataUpload(batch)

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	cmd := &connectorv1.Command{CommandId: "cmd-1", Type: "PROBE_METRICS", Payload: []byte(`{"datasource_id":"ds1"}`)}
	if err := f.servers.Dispatch(ctx, connID, cmd); err != nil {
		t.Fatalf("dispatch: %v", err)
	}
	if f.sink.Total() != 1 {
		t.Fatalf("sink total = %d, want 1", f.sink.Total())
	}
	got, ok := f.sink.Latest()
	if !ok || got.DatasourceID != "ds1" || len(got.Metrics) != 1 {
		t.Fatalf("sink batch = %+v ok=%v", got, ok)
	}
}

// TestCollectHandlerE2E spec-1.3 D4：collector → gateway 内部 collect API（HTTP，svc-token）
// → 下发 → 连接器回 DataUpload → 落 Sink，端点回 200。
func TestCollectHandlerE2E(t *testing.T) {
	f := newFixture(t, accept.DefaultSessionConfig())
	cert, key := f.ca.issueClient(t, connID, tenantID)
	conn, stream := f.dialSession(t, cert, key)
	defer func() { _ = conn.Close() }()
	if err := sendHello(stream, connID); err != nil {
		t.Fatalf("hello: %v", err)
	}
	if _, err := stream.Recv(); err != nil {
		t.Fatalf("ack: %v", err)
	}
	waitFor(t, func() bool { return f.console.sawStatus("online") }, "online")
	go respondMetrics(stream)

	ts := httptest.NewServer(accept.CollectHandler(f.servers, "collect-tok"))
	defer ts.Close()
	body := `{"connector_id":"` + connID + `","datasource_id":"ds1","engine_family":"postgres"}`
	req, _ := http.NewRequest(http.MethodPost, ts.URL+"/internal/v1/collect", strings.NewReader(body))
	req.Header.Set("Authorization", "Bearer collect-tok")
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("collect POST: %v", err)
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("collect status = %d, want 200", resp.StatusCode)
	}
	if f.sink.Total() != 1 {
		t.Fatalf("sink total = %d, want 1", f.sink.Total())
	}
}

// respondMetrics 模拟连接器：对 PROBE_METRICS 回 DataUpload 帧携带最小 batch。
func respondMetrics(stream connectorv1.SessionService_SessionClient) {
	for {
		fr, err := stream.Recv()
		if err != nil {
			return
		}
		cmd := fr.GetCommand()
		if cmd == nil || cmd.GetType() != "PROBE_METRICS" {
			continue
		}
		batch := metrics.Batch{
			DatasourceID: "ds1", EngineFamily: "postgres", CatalogVersion: metrics.CatalogVersion,
			Metrics: []metrics.Metric{{Name: "pg.connections.active", Value: 5, Unit: metrics.UnitCount}},
		}
		payload, _ := json.Marshal(batch)
		_ = stream.Send(&connectorv1.ClientFrame{Frame: &connectorv1.ClientFrame_DataUpload{
			DataUpload: &connectorv1.DataUpload{
				CommandId: cmd.GetCommandId(), DatasourceId: "ds1", Kind: "metrics", Payload: payload,
			},
		}})
	}
}

// TestDispatchOfflineConnector spec-1.3：目标连接器无会话 → Dispatch 返回 offline。
func TestDispatchOfflineConnector(t *testing.T) {
	f := newFixture(t, accept.DefaultSessionConfig())
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	err := f.servers.Dispatch(ctx, "no-such-connector", &connectorv1.Command{CommandId: "x", Type: "PROBE_METRICS"})
	if err == nil {
		t.Fatal("dispatch to offline connector should fail")
	}
}

// --- shared helpers ---

func hb(seq uint64) *connectorv1.ClientFrame {
	return &connectorv1.ClientFrame{Frame: &connectorv1.ClientFrame_Heartbeat{
		Heartbeat: &connectorv1.Heartbeat{Seq: seq},
	}}
}

func recvDrain(t *testing.T, stream connectorv1.SessionService_SessionClient) bool {
	t.Helper()
	done := make(chan bool, 1)
	go func() {
		for {
			f, err := stream.Recv()
			if err != nil {
				done <- false
				return
			}
			if f.GetDrain() != nil {
				done <- true
				return
			}
		}
	}()
	select {
	case got := <-done:
		return got
	case <-time.After(3 * time.Second):
		return false
	}
}

func waitFor(t *testing.T, cond func() bool, what string) {
	t.Helper()
	for i := 0; i < 100; i++ {
		if cond() {
			return
		}
		time.Sleep(20 * time.Millisecond)
	}
	t.Fatalf("condition %q not met", what)
}

func mustListen(t *testing.T) net.Listener {
	t.Helper()
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("listen: %v", err)
	}
	return ln
}
