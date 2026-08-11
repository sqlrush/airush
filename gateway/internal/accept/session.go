package accept

import (
	"context"
	"crypto/x509"
	"errors"
	"log/slog"
	"time"

	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/credentials"
	"google.golang.org/grpc/peer"
	"google.golang.org/grpc/status"

	"github.com/sqlrush/airush/gateway/internal/consoleclient"
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
	logger   *slog.Logger
	nowFn    func() time.Time
}

// NewSessionServer 构造。
func NewSessionServer(console *consoleclient.Client, cfg SessionConfig, logger *slog.Logger) *SessionServer {
	return &SessionServer{
		console: console, registry: newRegistry(), cfg: cfg, logger: logger,
		nowFn: time.Now,
	}
}

// DrainAll 供优雅下线调用（广播 Drain）。
func (s *SessionServer) DrainAll(reason string) { s.registry.drainAll(reason) }

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

	sess := &session{connectorID: hello.GetConnectorId(), tenantID: tenantID, drain: make(chan string, 1)}
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
		case f := <-frames:
			if hb := f.GetHeartbeat(); hb != nil {
				resetTimer(degrade, degradedAfter)
				resetTimer(offline, s.cfg.OfflineTimeout)
				s.setStatus(ctx, sess, "online", timePtr(s.nowFn()))
				_ = stream.Send(&connectorv1.ServerFrame{Frame: &connectorv1.ServerFrame_HeartbeatAck{
					HeartbeatAck: &connectorv1.HeartbeatAck{Seq: hb.GetSeq()},
				}})
			}
			// CommandResult 处理留 Stage 2 执行链；本 spec 仅 PING/ECHO 由测试驱动 Command 下发
		}
	}
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
