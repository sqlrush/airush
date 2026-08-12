package session

import (
	"context"

	connectorv1 "github.com/sqlrush/airush/proto/gen/go/connector/v1"
)

// ChainHandler 依次尝试各 handler，返回首个非 nil 结果（前置 handler 对不认识的
// 指令返回 nil 以透传，如 dbprobe 仅认 PROBE_METRICS）。末位应为 BuiltinHandler 兜底。
type ChainHandler []Handler

// Handle 依链尝试。
func (c ChainHandler) Handle(ctx context.Context, cmd *connectorv1.Command) *connectorv1.CommandResult {
	for _, h := range c {
		if res := h.Handle(ctx, cmd); res != nil {
			return res
		}
	}
	return BuiltinHandler{}.Handle(ctx, cmd) // 空链兜底
}

// BuiltinHandler 是 Stage 1 指令处理器：仅 PING/ECHO（spec-1.2 §1.2）。
// 采集/执行类指令的处理器随 spec-1.3/Stage 2 挂进同一 Handler 接口。
type BuiltinHandler struct{}

// Handle 分发指令；未知类型显式回 UNSUPPORTED（规则 6：不静默）。
func (BuiltinHandler) Handle(_ context.Context, cmd *connectorv1.Command) *connectorv1.CommandResult {
	switch cmd.GetType() {
	case "PING":
		return &connectorv1.CommandResult{
			CommandId: cmd.GetCommandId(),
			Status:    connectorv1.CommandResult_STATUS_OK,
		}
	case "ECHO":
		return &connectorv1.CommandResult{
			CommandId: cmd.GetCommandId(),
			Status:    connectorv1.CommandResult_STATUS_OK,
			Payload:   cmd.GetPayload(),
		}
	default:
		return &connectorv1.CommandResult{
			CommandId: cmd.GetCommandId(),
			Status:    connectorv1.CommandResult_STATUS_UNSUPPORTED,
			Error: &connectorv1.CommandError{
				Code:    "AR_COMMON_NOT_IMPLEMENTED",
				Message: "unsupported command type: " + cmd.GetType(),
			},
		}
	}
}
