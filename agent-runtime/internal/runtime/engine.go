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
	// LLMWireAPI 是 codexgo 客户端对网关说的线协议：WireAPIChat（缺省；chat/completions，LiteLLM 原生转发，
	// 工具回合 id 不经桥接改写）或 WireAPIResponses（Responses API，LiteLLM 桥接成 chat——spec-1.7 Q3-A，
	// 金丝雀实测桥接在 Kimi 工具回合上丢 tool_call_id → 退到 Q3 备选 B）。
	LLMWireAPI WireAPI
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
	cfg, err := cfg.withDefaults()
	if err != nil {
		return nil, err
	}
	e := &Engine{
		cfg: cfg, store: cfg.Store, logger: cfg.Logger, now: cfg.Now, approver: cfg.Approver,
		live: map[string]*liveThread{}, notifier: newNotifier(),
		limiter: multiagent.NewCountingExecutionLimiter(cfg.MaxSubAgents),
	}
	if cfg.MCP != nil {
		e.mcpTools = cfg.MCP.ListAllToolInfos()
	}
	// 平台供应商能力：无托管 web_search / 图片生成（LLM 网关是纯路由，这些托管工具不存在），
	// namespace tools（tool_search）保留。进程级设置：本进程只有这一个供应商。
	core.SetProviderCapabilitiesResolver(func(string) core.ProviderCapabilities {
		return core.ProviderCapabilities{NamespaceTools: true}
	})
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

// WireAPI 是模型客户端的线协议。
type WireAPI string

const (
	// WireAPIChat 是 OpenAI chat/completions（缺省）。
	WireAPIChat WireAPI = "chat"
	// WireAPIResponses 是 OpenAI Responses API。
	WireAPIResponses WireAPI = "responses"
)

// ParseWireAPI 解析配置值（空 → chat）。
func ParseWireAPI(s string) (WireAPI, error) {
	switch WireAPI(s) {
	case "", WireAPIChat:
		return WireAPIChat, nil
	case WireAPIResponses:
		return WireAPIResponses, nil
	default:
		return "", fmt.Errorf("unknown LLM wire api %q (chat|responses)", s)
	}
}

// withDefaults 校验必填项并填缺省（返回新值，不改入参）。
func (c Config) withDefaults() (Config, error) {
	if c.Store == nil {
		return c, errors.New("runtime: Store is required")
	}
	if c.LLMBaseURL == "" || c.LLMTransport == nil {
		return c, errors.New("runtime: LLMBaseURL and LLMTransport are required")
	}
	if c.DefaultModel == "" {
		c.DefaultModel = "chat-default"
	}
	if c.LLMWireAPI == "" {
		c.LLMWireAPI = WireAPIChat
	}
	if c.MaxSubAgents <= 0 {
		c.MaxSubAgents = 8
	}
	if c.HeartbeatInterval <= 0 {
		c.HeartbeatInterval = 15 * time.Second
	}
	if c.Logger == nil {
		c.Logger = slog.Default()
	}
	if c.Now == nil {
		c.Now = time.Now
	}
	if c.Approver == nil {
		c.Approver = approvals.DenyActions{}
	}
	return c, nil
}
