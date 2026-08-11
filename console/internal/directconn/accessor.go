package directconn

import (
	"context"

	"github.com/sqlrush/airush/libs/accessor"
)

// DirectAccessor 是 Direct 通道的 accessor.Accessor 实现（spec-1.17 D4）。
// Stage 1 仅只读 PING/ECHO（经 BuiltinDispatch，与 Connector 通道语义一致）；
// 采集探针类型随 spec-1.3 增补，动作类由 Stage 2 审批链解锁——本实现对写路径硬拒。
type DirectAccessor struct {
	mgr          *Manager
	datasourceID string
}

// 编译期断言：DirectAccessor 满足通道无关接口（spec-1.17 T1）。
var _ accessor.Accessor = (*DirectAccessor)(nil)

// AccessorFor 返回绑定某 datasource 的 Direct 接入器。
func (m *Manager) AccessorFor(datasourceID string) *DirectAccessor {
	return &DirectAccessor{mgr: m, datasourceID: datasourceID}
}

// Dispatch 分发指令。Stage 1 全部命令走 BuiltinDispatch（只读护栏）；
// 建连由 poolFor 惰性完成，仅在需要触库的探针类型（spec-1.3）时触发。
func (a *DirectAccessor) Dispatch(_ context.Context, cmd accessor.Command) (accessor.Result, error) {
	return accessor.BuiltinDispatch(cmd), nil
}

// Close 释放该 datasource 的直连池。
func (a *DirectAccessor) Close() error {
	a.mgr.Destroy(a.datasourceID)
	return nil
}
