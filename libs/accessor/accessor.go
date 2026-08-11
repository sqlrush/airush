// Package accessor is the channel-agnostic接入器 abstraction (spec-1.17 D1).
// Connector（反向隧道）与 Direct（平台直连）两种接入通道对同一 Accessor 接口，
// 使上层逻辑（探针 spec-1.3、诊断）通道无关。
//
// 位置说明（实施修订，spec-1.17 §1.3）：spec 原写 connector/internal/accessor，
// 但 depguard 禁止 console import connector（console 承载 Direct 实现），故接口移至
// libs/accessor——console/connector/agent-runtime 均可自由 import libs，边界干净。
package accessor

import "context"

// Command 是通道无关的指令（Stage 1：PING/ECHO；采集探针类型随 spec-1.3 增补）。
type Command struct {
	// ID 关联请求与结果（幂等/审计）。
	ID string
	// Type 指令类型；只读类由 BuiltinDispatch 处理，动作类 Stage 1 硬拒。
	Type string
	// Payload 指令载荷（类型相关）。
	Payload []byte
}

// Status 是指令执行结果状态。
type Status int

const (
	// StatusUnspecified 是零值占位。
	StatusUnspecified Status = iota
	// StatusOK 执行成功。
	StatusOK
	// StatusError 执行失败（Code/Message 载明）。
	StatusError
	// StatusUnsupported 指令类型不受支持（未实现/越权）。
	StatusUnsupported
)

// Result 是指令执行结果（通道无关）。
type Result struct {
	CommandID string
	Status    Status
	Payload   []byte
	// Code 为失败时的注册错误码（proto/errors.json 空间）。
	Code    string
	Message string
}

// Accessor 是接入通道的统一执行面：给定指令产出结果。
// Connector 实现在客户侧执行；Direct 实现在平台侧对直连库执行。
// Probe 采集入口由 spec-1.3 在 Dispatch 之上增补指令类型，不改本接口。
type Accessor interface {
	// Dispatch 执行一条指令并返回结果。传输/连接层错误经 error 返回；
	// 业务失败经 Result.Status/Code 表达。
	Dispatch(ctx context.Context, cmd Command) (Result, error)
	// Close 释放通道资源（连接/会话）。
	Close() error
}

// Stage 1 支持的只读指令类型。
const (
	CommandPing = "PING"
	CommandEcho = "ECHO"
)

// codeNotImplemented 是动作类/未知指令的统一出口（proto/errors.json）。
const codeNotImplemented = "AR_COMMON_NOT_IMPLEMENTED"

// BuiltinDispatch 是 Stage 1 通道无关的指令分发器：仅 PING/ECHO 只读指令；
// 其余（含动作类）显式 StatusUnsupported + AR_COMMON_NOT_IMPLEMENTED（规则 6：
// 不静默）。Connector 与 Direct 实现共享此逻辑,保证两通道语义一致(spec-1.17 §3)。
func BuiltinDispatch(cmd Command) Result {
	switch cmd.Type {
	case CommandPing:
		return Result{CommandID: cmd.ID, Status: StatusOK}
	case CommandEcho:
		return Result{CommandID: cmd.ID, Status: StatusOK, Payload: cmd.Payload}
	default:
		return Result{
			CommandID: cmd.ID,
			Status:    StatusUnsupported,
			Code:      codeNotImplemented,
			Message:   "unsupported command type: " + cmd.Type,
		}
	}
}
