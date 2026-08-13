// Package accept 是 gateway 的 Connector 接入面（spec-1.2 D4）：enrollment/session
// gRPC 服务 + 会话注册表 + 心跳状态机。
package accept

import (
	"sync"

	connectorv1 "github.com/sqlrush/airush/proto/gen/go/connector/v1"
)

// session 是一个活跃会话句柄。
type session struct {
	connectorID string
	tenantID    string
	drain       chan string               // 关闭信号（写入 Drain reason 后关闭 stream）
	commands    chan *connectorv1.Command // 平台下发的待发指令（loop 取出经 stream 发出）

	pmu     sync.Mutex
	pending map[string]chan error // command_id → 结果信号（nil=成功；err=失败码）
}

func newSession(connectorID, tenantID string) *session {
	return &session{
		connectorID: connectorID, tenantID: tenantID,
		drain:    make(chan string, 1),
		commands: make(chan *connectorv1.Command, 4),
		pending:  make(map[string]chan error),
	}
}

// awaitCommand 登记一个待关联信号通道；调用方发送 command 后在返回的通道上等结果
// （spec-1.3：DataUpload 成功回 nil，CommandResult(error) 回错误码）。
func (s *session) awaitCommand(commandID string) chan error {
	ch := make(chan error, 1)
	s.pmu.Lock()
	s.pending[commandID] = ch
	s.pmu.Unlock()
	return ch
}

// signalCommand 通知等待方指令终态（无等待方则丢弃，如异步自采上报）。
func (s *session) signalCommand(commandID string, err error) {
	s.pmu.Lock()
	ch, ok := s.pending[commandID]
	delete(s.pending, commandID)
	s.pmu.Unlock()
	if ok {
		ch <- err
	}
}

// cancelCommand 撤销登记（超时/发送失败时防泄漏）。
func (s *session) cancelCommand(commandID string) {
	s.pmu.Lock()
	delete(s.pending, commandID)
	s.pmu.Unlock()
}

// registry 保证同 connector 会话唯一（新连踢旧连，spec-1.2 §3 / T8）。
type registry struct {
	mu       sync.Mutex
	sessions map[string]*session // connectorID → 当前会话
}

func newRegistry() *registry {
	return &registry{sessions: make(map[string]*session)}
}

// add 登记新会话；若已有同 connector 会话，向其 drain 发信号并替换。
func (r *registry) add(s *session) {
	r.mu.Lock()
	defer r.mu.Unlock()
	if old, ok := r.sessions[s.connectorID]; ok {
		signalDrain(old, "superseded by new session")
	}
	r.sessions[s.connectorID] = s
}

// remove 注销会话（仅当仍是当前登记者，防踢旧连后误删新连）。
func (r *registry) remove(s *session) {
	r.mu.Lock()
	defer r.mu.Unlock()
	if cur, ok := r.sessions[s.connectorID]; ok && cur == s {
		delete(r.sessions, s.connectorID)
	}
}

// get 按 connectorID 取当前会话。
func (r *registry) get(connectorID string) (*session, bool) {
	r.mu.Lock()
	defer r.mu.Unlock()
	s, ok := r.sessions[connectorID]
	return s, ok
}

// drainAll 广播 Drain（网关优雅下线，spec-1.2 §6）。
func (r *registry) drainAll(reason string) {
	r.mu.Lock()
	defer r.mu.Unlock()
	for _, s := range r.sessions {
		signalDrain(s, reason)
	}
}

// signalDrain 非阻塞发送 drain 信号（channel 容量 1，重复信号丢弃）。
func signalDrain(s *session, reason string) {
	select {
	case s.drain <- reason:
	default:
	}
}

var _ = connectorv1.Drain{} // 帧类型占位引用（drain reason 写入 ServerFrame_Drain）
