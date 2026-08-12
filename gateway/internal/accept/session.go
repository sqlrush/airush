package accept

import (
	"context"
	"crypto/x509"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"time"

	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/credentials"
	"google.golang.org/grpc/peer"
	"google.golang.org/grpc/status"

	"github.com/sqlrush/airush/gateway/internal/consoleclient"
	"github.com/sqlrush/airush/libs/metrics"
	connectorv1 "github.com/sqlrush/airush/proto/gen/go/connector/v1"
)

// SessionConfig 会话参数（默认值由 cmd 提供，spec-1.2 §2.2）。
type SessionConfig struct {
	HeartbeatInterval time.Duration
	MissedBeatsDegr   int // 缺 N 个心跳 → degraded
	OfflineTimeout    time.Duration
}

// DefaultSessionConfig spec-1.2 §2.2/§2.4 定版默认。
func DefaultSessionConfig() SessionConfig {
	return SessionConfig{
		HeartbeatInterval: 15 * time.Second,
		MissedBeatsDegr:   3,
		OfflineTimeout:    5 * time.Minute,
	}
}

// SessionServer 实现 mTLS 会话 RPC。
type SessionServer struct {
	connectorv1.UnimplementedSessionServiceServer
	console  *consoleclient.Client
	registry *registry
	cfg      SessionConfig
	sink     metrics.Sink // DataUpload 落点（spec-1.3 §2.4：gateway 转 Sink）
	logger   *slog.Logger
	nowFn    func() time.Time
}

// NewSessionServer 构造。sink 为 nil 时用丢弃 Sink（无 Connector 采集消费者的形态）。
func NewSessionServer(console *consoleclient.Client, cfg SessionConfig, sink metrics.Sink, logger *slog.Logger) *SessionServer {
	if sink == nil {
		sink = discardSink{}
	}
	return &SessionServer{
		console: console, registry: newRegistry(), cfg: cfg, sink: sink, logger: logger,
		nowFn: time.Now,
	}
}

// discardSink 在未配置消费者时吞掉上报（保证 loop 不 panic；生产必注入 buffer/TSDB Sink）。
type discardSink struct{}

func (discardSink) Publish(context.Context, metrics.Batch) error { return nil }

// DrainAll 供优雅下线调用（广播 Drain）。
func (s *SessionServer) DrainAll(reason string) { s.registry.drainAll(reason) }

// ErrConnectorOffline 表示目标连接器无活跃会话。
var ErrConnectorOffline = errors.New("connector has no active session")

// Dispatch 向指定连接器下发一条指令并等待其终态（spec-1.3 平台驱动采集）：成功回 nil
// （采集数据已由 loop 收 DataUpload 落 Sink），失败回错误码，连接器离线回 ErrConnectorOffline，
// 超时由 ctx 控制。
func (s *SessionServer) Dispatch(ctx context.Context, connectorID string, cmd *connectorv1.Command) error {
	sess, ok := s.registry.get(connectorID)
	if !ok {
		return ErrConnectorOffline
	}
	sig := sess.awaitCommand(cmd.GetCommandId())
	select {
	case sess.commands <- cmd:
	case <-ctx.Done():
		sess.cancelCommand(cmd.GetCommandId())
		return ctx.Err()
	}
	select {
	case err := <-sig:
		return err
	case <-ctx.Done():
		sess.cancelCommand(cmd.GetCommandId())
		return ctx.Err()
	}
}

// Session 是 bidi stream 主循环。
func (s *SessionServer) Session(stream connectorv1.SessionService_SessionServer) error {
	ctx := stream.Context()
	peerCert, err := clientCert(ctx)
	if err != nil {
		return status.Error(codes.Unauthenticated, "AR_AUTH_UNAUTHENTICATED")
	}

	hello, err := recvHello(stream)
	if err != nil {
		return err
	}
	// 证书 CN 必须与 Hello.connector_id 一致（spec-1.2 §3）
	if peerCert.Subject.CommonName != hello.GetConnectorId() {
		return status.Error(codes.PermissionDenied, "AR_AUTH_FORBIDDEN")
	}
	tenantID, err := tenantFromCert(peerCert)
	if err != nil {
		return status.Error(codes.PermissionDenied, "AR_AUTH_FORBIDDEN")
	}
	fingerprint := certFingerprint(peerCert)

	// 握手校验（指纹 + 状态）经 console
	if err := s.console.Handshake(ctx, tenantID, hello.GetConnectorId(), fingerprint); err != nil {
		return handshakeGRPCError(err)
	}

	sess := newSession(hello.GetConnectorId(), tenantID)
	s.registry.add(sess)
	defer s.registry.remove(sess)
	s.setStatus(ctx, sess, "online", timePtr(s.nowFn()))
	defer s.setStatus(context.Background(), sess, "offline", nil)

	if err := stream.Send(&connectorv1.ServerFrame{Frame: &connectorv1.ServerFrame_HelloAck{
		HelloAck: &connectorv1.HelloAck{
			HeartbeatIntervalSeconds: uint32(s.cfg.HeartbeatInterval.Seconds()),
		},
	}}); err != nil {
		return err
	}

	return s.loop(stream, sess)
}

// loop 处理心跳/指令结果与 drain/超时（状态机在此驱动，spec-1.2 §2.4）。
func (s *SessionServer) loop(stream connectorv1.SessionService_SessionServer, sess *session) error {
	ctx := stream.Context()
	frames := make(chan *connectorv1.ClientFrame)
	recvErr := make(chan error, 1)
	go func() {
		for {
			f, err := stream.Recv()
			if err != nil {
				recvErr <- err
				return
			}
			frames <- f
		}
	}()

	// degraded：缺 N 个心跳周期无消息；offline：更长的静默超时。两级定时器。
	degradedAfter := time.Duration(s.cfg.MissedBeatsDegr) * s.cfg.HeartbeatInterval
	degrade := time.NewTimer(degradedAfter)
	offline := time.NewTimer(s.cfg.OfflineTimeout)
	defer degrade.Stop()
	defer offline.Stop()

	for {
		select {
		case reason := <-sess.drain:
			_ = stream.Send(&connectorv1.ServerFrame{Frame: &connectorv1.ServerFrame_Drain{
				Drain: &connectorv1.Drain{Reason: reason},
			}})
			return status.Error(codes.Aborted, "session drained") // 正常重连信号，非 apierror 码
		case <-ctx.Done():
			return ctx.Err()
		case <-recvErr:
			return nil // 客户端断开：defer 置 offline
		case <-degrade.C:
			s.setStatus(ctx, sess, "degraded", nil)
		case <-offline.C:
			s.setStatus(context.Background(), sess, "offline", nil)
			return status.Error(codes.DeadlineExceeded, "AR_CONNECTOR_OFFLINE")
		case cmd := <-sess.commands:
			// 平台下发指令（spec-1.3 采集等）：经 stream 发给连接器，终态由回帧关联回。
			if err := stream.Send(&connectorv1.ServerFrame{Frame: &connectorv1.ServerFrame_Command{
				Command: cmd,
			}}); err != nil {
				sess.signalCommand(cmd.GetCommandId(), fmt.Errorf("send command: %w", err))
			}
		case f := <-frames:
			s.onClientFrame(ctx, stream, sess, f, degrade, offline, degradedAfter)
		}
	}
}

// onClientFrame 处理连接器上行帧：心跳（状态机 + ack）、DataUpload（落 Sink，spec-1.3）、
// CommandResult（关联回触发方，PING/ECHO 或采集失败）。
func (s *SessionServer) onClientFrame(ctx context.Context, stream connectorv1.SessionService_SessionServer,
	sess *session, f *connectorv1.ClientFrame, degrade, offline *time.Timer, degradedAfter time.Duration,
) {
	if hb := f.GetHeartbeat(); hb != nil {
		resetTimer(degrade, degradedAfter)
		resetTimer(offline, s.cfg.OfflineTimeout)
		s.setStatus(ctx, sess, "online", timePtr(s.nowFn()))
		_ = stream.Send(&connectorv1.ServerFrame{Frame: &connectorv1.ServerFrame_HeartbeatAck{
			HeartbeatAck: &connectorv1.HeartbeatAck{Seq: hb.GetSeq()},
		}})
	}
	if du := f.GetDataUpload(); du != nil {
		s.handleDataUpload(ctx, sess, du) // spec-1.3：DataUpload → Sink + 关联回触发方
	}
	if res := f.GetCommandResult(); res != nil {
		sess.signalCommand(res.GetCommandId(), commandResultErr(res))
	}
}

// handleDataUpload 落库 DataUpload 携带的 batch 到 Sink，并关联回触发方（spec-1.3 T11）。
// AD-3：只接受结构化 batch；payload 非法/落 Sink 失败都回错误码给触发方（fail-closed）。
func (s *SessionServer) handleDataUpload(ctx context.Context, sess *session, du *connectorv1.DataUpload) {
	var batch metrics.Batch
	if err := json.Unmarshal(du.GetPayload(), &batch); err != nil {
		s.logger.Warn("data upload decode failed",
			"connector_id", sess.connectorID, "kind", du.GetKind(), "err", err)
		sess.signalCommand(du.GetCommandId(), fmt.Errorf("AR_METRICS_COLLECT_FAILED: decode batch"))
		return
	}
	if err := s.sink.Publish(ctx, batch); err != nil {
		s.logger.Warn("data upload sink publish failed", "connector_id", sess.connectorID, "err", err)
		sess.signalCommand(du.GetCommandId(), fmt.Errorf("AR_METRICS_COLLECT_FAILED: sink publish"))
		return
	}
	sess.signalCommand(du.GetCommandId(), nil)
}

// commandResultErr 把连接器回的 CommandResult 映射为 Dispatch 信号：非 OK 转错误码。
func commandResultErr(res *connectorv1.CommandResult) error {
	if res.GetStatus() == connectorv1.CommandResult_STATUS_OK {
		return nil
	}
	code := res.GetError().GetCode()
	if code == "" {
		code = "AR_INTERNAL_ERROR"
	}
	return errors.New(code)
}

// resetTimer 安全重置（先停并排空，避免旧信号误触发）。
func resetTimer(t *time.Timer, d time.Duration) {
	if !t.Stop() {
		select {
		case <-t.C:
		default:
		}
	}
	t.Reset(d)
}

func (s *SessionServer) setStatus(ctx context.Context, sess *session, st string, hb *time.Time) {
	if err := s.console.ReportStatus(ctx, sess.tenantID, sess.connectorID, st, hb); err != nil {
		s.logger.Warn("report status failed", "connector_id", sess.connectorID, "status", st, "err", err)
	}
}

func recvHello(stream connectorv1.SessionService_SessionServer) (*connectorv1.Hello, error) {
	f, err := stream.Recv()
	if err != nil {
		return nil, status.Error(codes.Unauthenticated, "AR_AUTH_UNAUTHENTICATED")
	}
	hello := f.GetHello()
	if hello == nil || hello.GetConnectorId() == "" {
		return nil, status.Error(codes.InvalidArgument, "AR_VALIDATION_FAILED")
	}
	return hello, nil
}

func clientCert(ctx context.Context) (*x509.Certificate, error) {
	p, ok := peer.FromContext(ctx)
	if !ok {
		return nil, errors.New("no peer")
	}
	tlsInfo, ok := p.AuthInfo.(credentials.TLSInfo)
	if !ok || len(tlsInfo.State.PeerCertificates) == 0 {
		return nil, errors.New("no client certificate")
	}
	return tlsInfo.State.PeerCertificates[0], nil
}

func handshakeGRPCError(err error) error {
	var apiErr *consoleclient.APIError
	if errors.As(err, &apiErr) {
		if apiErr.Status == 401 || apiErr.Status == 403 {
			return status.Error(codes.PermissionDenied, apiErr.Code)
		}
		if apiErr.Status == 404 || apiErr.Status == 409 {
			return status.Error(codes.FailedPrecondition, apiErr.Code)
		}
	}
	return status.Error(codes.Unavailable, "AR_INTERNAL_ERROR")
}

func timePtr(t time.Time) *time.Time { return &t }
