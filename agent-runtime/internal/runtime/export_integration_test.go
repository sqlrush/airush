//go:build integration

package runtime

import (
	"context"
	"testing"
)

// 给 runtime_test 包（跨包 API 集成用例）暴露本包的集成夹具。

// NewTestTenant 建租户主档，返回租户 ctx 与 id。
func NewTestTenant(t *testing.T) (context.Context, string) { return newTenant(t) }

// TestLLM 是进程内 Responses 假供应商句柄。
type TestLLM = fakeLLM

// NewTestLLM 起一个假供应商。
func NewTestLLM(t *testing.T) *TestLLM { return newFakeLLM(t) }

// NewTestEngine 装配指向假供应商的 Engine。
func NewTestEngine(t *testing.T, llmSrv *TestLLM, pod string) *Engine {
	e, _ := newEngine(t, llmSrv, pod)
	return e
}
