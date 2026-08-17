// Package runtime 是 agent-runtime 的运行时层（spec-1.8 D3/D4/D5）：把 codexgo 抽核
// （ThreadManager / Codex）装配成平台拥有的 AgentCore 接口（decoupling R1），会话状态全部外置到
// pgstore（AD-1：进程内只有正在执行的 turn 的瞬时状态），租户经 tenantctx 贯穿。
package runtime

import (
	"context"
	"encoding/json"

	"github.com/sqlrush/codexgo/pkg/protocol"
)

// StartThreadInput 建线程参数（租户来自 ctx，不在结构体里——ctx 是唯一租户来源）。
type StartThreadInput struct {
	// AgentID 可空（空 = 无 agent 归属的自由会话；spec-1.1 助理 agent 为系统内置行）。
	AgentID string
	// Model 逻辑模型名；空 → agent.default_model → 运行时缺省。
	Model string
	Title string
}

// ThreadRef 是建线程结果。
type ThreadRef struct {
	ThreadID string
}

// TurnInput 是一轮输入（codexgo protocol.UserInput：text / image …）。
type TurnInput struct {
	Items []protocol.UserInput
}

// TurnRef 是 SubmitTurn 结果：Queued=true 表示进了队列（steer 到运行中的 turn、或等待租户并发额度、
// 或等待持有该线程的 pod 领取），TurnID 在已被接纳时非空。
type TurnRef struct {
	TurnID string
	Queued bool
}

// Event 是事件流一条（租户安全投影：不含租户列，只有线程内可见的字段）。
type Event struct {
	Seq        int64           `json:"seq"`
	TurnID     string          `json:"turn_id,omitempty"`
	Type       string          `json:"type"`
	Payload    json.RawMessage `json:"payload"`
	PayloadRef string          `json:"payload_ref,omitempty"`
}

// AgentCore 是会话调度器 ↔ agent core 的唯一接口（spec-1.8 §2.2；换 core 只换实现）。
type AgentCore interface {
	StartThread(ctx context.Context, in StartThreadInput) (ThreadRef, error)
	SubmitTurn(ctx context.Context, threadID string, in TurnInput) (TurnRef, error)
	Interrupt(ctx context.Context, threadID string) error
	Events(ctx context.Context, threadID string, fromSeq int64) (<-chan Event, error)
	ResumeThread(ctx context.Context, threadID string) error
}

// queuedPayload 是外置队列（agent_thread_queue.payload）的形状：
// user_input 携带输入项；interrupt 是跨 pod 的中断指令（持有线程的 pod 领取后执行）。
type queuedPayload struct {
	Type  string               `json:"type"`
	Items []protocol.UserInput `json:"items,omitempty"`
}

const (
	queuedUserInput = "user_input"
	queuedInterrupt = "interrupt"
)
