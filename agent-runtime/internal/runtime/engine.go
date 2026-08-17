package runtime

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"sync"
	"time"

	"github.com/sqlrush/codexgo/pkg/core"
	"github.com/sqlrush/codexgo/pkg/multiagent"
	"github.com/sqlrush/codexgo/pkg/protocol"
	"github.com/sqlrush/codexgo/pkg/rollout"
	"github.com/sqlrush/codexgo/pkg/tools"

	"github.com/sqlrush/airush/agent-runtime/internal/approvals"
	"github.com/sqlrush/airush/agent-runtime/internal/pgstore"
	"github.com/sqlrush/airush/libs/apierror"
)

// Config 装配 Engine。
type Config struct {
	Store *pgstore.Store
	// DefaultModel 是线程未指定模型时的逻辑名（spec-1.7；缺省 chat-default）。
	DefaultModel string
	// LLMBaseURL 是 LLM 网关（LiteLLM）的 OpenAI 兼容根（…/v1）；LLMTransport 是挂了
	// libs/llm.Meter 的 RoundTripper（配额门 + 记账 + Authorization 由它注入）。
	LLMBaseURL   string
	LLMTransport http.RoundTripper
	// MCP 是已启动的 MCP 管理器（静态 endpoints；nil = 无 skill 工具）。*mcp.Manager 满足。
	MCP MCPGateway
	// Approver 是审批阶段（AD-9）；nil 用 Stage 1 的 DenyActions。
	Approver approvals.Approver
	// PodName 记进 agent_threads.running_pod（排水/恢复用）。
	PodName string
	// MaxSubAgents 是每个根线程可派生的子 agent 上限（0 = 缺省 8）。
	MaxSubAgents int
	// HeartbeatInterval 是持有线程期间的心跳周期（0 = 15s）；恢复扫描按 2× 判孤儿。
	HeartbeatInterval time.Duration
	Logger            *slog.Logger
	Now               func() time.Time
}

// Engine 实现 AgentCore：codexgo ThreadManager + pgstore 持久层 + 本 pod 的在飞会话表。
type Engine struct {
	cfg      Config
	store    *pgstore.Store
	logger   *slog.Logger
	now      func() time.Time
	approver approvals.Approver
	mcpTools []tools.McpToolInfo
	limiter  *multiagent.CountingExecutionLimiter
	// limiterT 是每租户并发 turn 上限（SetLimiter 注入；nil = 不限）。
	limiterT Limiter

	tm *core.ThreadManager

	mu   sync.Mutex
	live map[string]*liveThread
	// draining 置位后不再领取新 turn（D5 preStop）。
	draining bool

	notifier *notifier
}

// New 装配 Engine（不启动任何后台循环；见 Run）。
func New(cfg Config) (*Engine, error) {
	if cfg.Store == nil {
		return nil, errors.New("runtime: Store is required")
	}
	if cfg.LLMBaseURL == "" || cfg.LLMTransport == nil {
		return nil, errors.New("runtime: LLMBaseURL and LLMTransport are required")
	}
	if cfg.DefaultModel == "" {
		cfg.DefaultModel = "chat-default"
	}
	if cfg.MaxSubAgents <= 0 {
		cfg.MaxSubAgents = 8
	}
	if cfg.HeartbeatInterval <= 0 {
		cfg.HeartbeatInterval = 15 * time.Second
	}
	if cfg.Logger == nil {
		cfg.Logger = slog.Default()
	}
	if cfg.Now == nil {
		cfg.Now = time.Now
	}
	if cfg.Approver == nil {
		cfg.Approver = approvals.DenyActions{}
	}
	e := &Engine{
		cfg: cfg, store: cfg.Store, logger: cfg.Logger, now: cfg.Now, approver: cfg.Approver,
		live: map[string]*liveThread{}, notifier: newNotifier(),
		limiter: multiagent.NewCountingExecutionLimiter(cfg.MaxSubAgents),
	}
	if cfg.MCP != nil {
		e.mcpTools = cfg.MCP.ListAllToolInfos()
	}
	tm, err := core.NewThreadManager(core.ThreadManagerConfig{
		Store:           cfg.Store.Threads(),
		ServicesFactory: e.buildServices,
		NewThreadID:     protocol.NewThreadIDV7,
		SessionSource:   airushSessionSource(),
		InstallationID:  cfg.PodName,
		Originator:      originator,
		CliVersion:      cliVersion,
		Now:             cfg.Now,
	})
	if err != nil {
		return nil, fmt.Errorf("runtime: build thread manager: %w", err)
	}
	e.tm = tm
	return e, nil
}

const (
	originator = "airush-agent-runtime"
	cliVersion = "spec-1.8"
	// sessionSourceName 是 rollout SessionSource 的自定义名（session_meta.source）。
	sessionSourceName = "airush"
)

func airushSessionSource() rollout.SessionSource {
	return rollout.SessionSource{Kind: rollout.SessionSourceKindCustom, Custom: sessionSourceName}
}

// Store 暴露持久层（API 层的只读列表/历史直接读 store，不经 core）。
func (e *Engine) Store() *pgstore.Store { return e.store }

// LiveCount 是本 pod 在飞会话数（就绪探针 / 排水观测）。
func (e *Engine) LiveCount() int {
	e.mu.Lock()
	defer e.mu.Unlock()
	return len(e.live)
}

// Draining 报告是否在排水。
func (e *Engine) Draining() bool {
	e.mu.Lock()
	defer e.mu.Unlock()
	return e.draining
}

// tenantOf 取 ctx 租户；缺失 → AR_TENANT_CONTEXT_MISSING（fail-closed）。
func tenantOf(ctx context.Context) (string, error) {
	id, ok := tenantIDFrom(ctx)
	if !ok {
		return "", apierror.New(apierror.CodeTenantContextMissing)
	}
	return id, nil
}
