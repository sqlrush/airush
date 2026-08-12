// Package session 是 connector 会话循环（spec-1.2 D5 客户侧）：mTLS 建连、
// Hello/心跳、指令分发（Stage 1 仅 PING/ECHO），断线指数退避重连。
package session

import (
	"context"
	"fmt"
	"log/slog"
	"time"

	"google.golang.org/grpc"
	"google.golang.org/grpc/credentials"

	connectorv1 "github.com/sqlrush/airush/proto/gen/go/connector/v1"
)

// Config 会话客户端参数。
type Config struct {
	GatewayAddr string
	ConnectorID string
	Version     string
	// 退避序列上限（spec-1.2 §6：上限 5min + jitter）。
	MaxBackoff time.Duration
}

// Handler 指令处理器（通道无关，spec-1.17 直连接入器复用同一接口）。
type Handler interface {
	Handle(ctx context.Context, cmd *connectorv1.Command) *connectorv1.CommandResult
}

// Client 是会话循环。
type Client struct {
	cfg     Config
	creds   credentials.TransportCredentials
	handler Handler
	logger  *slog.Logger
	backoff *backoff
}

// New 构造。
func New(cfg Config, creds credentials.TransportCredentials, handler Handler, logger *slog.Logger) *Client {
	if cfg.MaxBackoff == 0 {
		cfg.MaxBackoff = 5 * time.Minute
	}
	return &Client{
		cfg: cfg, creds: creds, handler: handler, logger: logger,
		backoff: newBackoff(cfg.MaxBackoff),
	}
}

// Run 持续维持会话直到 ctx 取消；断线自动重连（Drain 视为服务端要求的重连）。
func (c *Client) Run(ctx context.Context) error {
	for {
		if ctx.Err() != nil {
			return ctx.Err()
		}
		err := c.runOnce(ctx)
		if ctx.Err() != nil {
			return ctx.Err()
		}
		delay := c.backoff.next()
		c.logger.Warn("session ended, reconnecting", "err", err, "backoff", delay)
		select {
		case <-time.After(delay):
		case <-ctx.Done():
			return ctx.Err()
		}
	}
}

// runOnce 建立一次会话并处理帧，直到出错/断开/Drain。成功握手后重置退避。
func (c *Client) runOnce(ctx context.Context) error {
	conn, err := grpc.NewClient(c.cfg.GatewayAddr, grpc.WithTransportCredentials(c.creds))
	if err != nil {
		return fmt.Errorf("session: dial: %w", err)
	}
	defer func() { _ = conn.Close() }()

	stream, err := connectorv1.NewSessionServiceClient(conn).Session(ctx)
	if err != nil {
		return fmt.Errorf("session: open stream: %w", err)
	}
	if err := stream.Send(&connectorv1.ClientFrame{Frame: &connectorv1.ClientFrame_Hello{
		Hello: &connectorv1.Hello{ConnectorId: c.cfg.ConnectorID, Version: c.cfg.Version},
	}}); err != nil {
		return fmt.Errorf("session: send hello: %w", err)
	}
	ack, err := stream.Recv()
	if err != nil {
		return fmt.Errorf("session: recv hello ack: %w", err)
	}
	interval := 15 * time.Second
	if hi := ack.GetHelloAck().GetHeartbeatIntervalSeconds(); hi > 0 {
		interval = time.Duration(hi) * time.Second
	}
	c.backoff.reset() // 握手成功
	c.logger.Info("session established", "connector_id", c.cfg.ConnectorID, "heartbeat", interval)

	return c.pump(ctx, stream, interval)
}

// pump 单发送方模型：唯一主循环负责全部 stream.Send（心跳 + 指令结果），
// recv 在独立 goroutine 只读并把待发结果经 channel 交回主循环——gRPC 客户端流
// SendMsg 非并发安全，不可从多 goroutine 调用。
func (c *Client) pump(ctx context.Context, stream connectorv1.SessionService_SessionClient, interval time.Duration) error {
	recvErr := make(chan error, 1)
	results := make(chan *connectorv1.CommandResult, 4)
	go func() {
		for {
			frame, err := stream.Recv()
			if err != nil {
				recvErr <- err
				return
			}
			if cmd := frame.GetCommand(); cmd != nil {
				results <- c.handler.Handle(ctx, cmd)
			}
			if d := frame.GetDrain(); d != nil {
				recvErr <- fmt.Errorf("session: drained: %s", d.GetReason())
				return
			}
		}
	}()

	ticker := time.NewTicker(interval)
	defer ticker.Stop()
	var seq uint64
	for {
		select {
		case <-ctx.Done():
			return ctx.Err()
		case err := <-recvErr:
			return err
		case res := <-results:
			if err := stream.Send(&connectorv1.ClientFrame{Frame: &connectorv1.ClientFrame_CommandResult{
				CommandResult: res,
			}}); err != nil {
				return fmt.Errorf("session: send command result: %w", err)
			}
		case <-ticker.C:
			seq++
			if err := stream.Send(&connectorv1.ClientFrame{Frame: &connectorv1.ClientFrame_Heartbeat{
				Heartbeat: &connectorv1.Heartbeat{Seq: seq},
			}}); err != nil {
				return fmt.Errorf("session: send heartbeat: %w", err)
			}
		}
	}
}
