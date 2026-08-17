package runtime

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"time"

	"github.com/sqlrush/codexgo/pkg/api"
	"github.com/sqlrush/codexgo/pkg/client"
	"github.com/sqlrush/codexgo/pkg/core"
	"github.com/sqlrush/codexgo/pkg/modelsmanager"
	"github.com/sqlrush/codexgo/pkg/multiagent"
	"github.com/sqlrush/codexgo/pkg/protocol"
	"github.com/sqlrush/codexgo/pkg/rollout"
	"github.com/sqlrush/codexgo/pkg/threadstore"

	"github.com/sqlrush/airush/agent-runtime/internal/pgstore"
	"github.com/sqlrush/airush/libs/obs"
)

// buildServices 是 codexgo ThreadServicesFactory：每次 spawn（新线程、resume、子 agent）
// 构造该线程的模型客户端 / 工具路由 / 持久化 recorder。ctx 必须携带租户——它来自 SubmitTurn
// 的会话 ctx，或子 agent 派生时父线程的 turn ctx。
func (e *Engine) buildServices(ctx context.Context, threadID protocol.ThreadID, cfg core.SessionConfiguration) (core.SessionServices, error) {
	if _, err := tenantOf(ctx); err != nil {
		return core.SessionServices{}, err
	}
	// 子 agent 由 core 直接 spawn，线程行还不存在：在这里补建（已存在 → 无操作）。
	if err := e.ensureThreadRow(ctx, threadID, cfg); err != nil {
		return core.SessionServices{}, err
	}
	if _, isSub := cfg.SessionSource.(rollout.SessionSource); isSub && e.liveFor(threadID) == nil {
		// core 直接 spawn 的子 agent：runtime 不持有它，起个泵排空事件队列。
		go e.pumpChild(threadID)
	}
	modelClient, err := e.newModelClient(threadID, cfg)
	if err != nil {
		return core.SessionServices{}, err
	}
	router, err := e.newToolRouter(threadID)
	if err != nil {
		return core.SessionServices{}, err
	}
	return core.SessionServices{
		ModelClient:     modelClient,
		ToolRouter:      router,
		ModelsManager:   staticModels{defaultSlug: e.cfg.DefaultModel},
		RolloutRecorder: &pgRecorder{store: e.store, threadID: threadID, notify: func() { e.notifier.Notify(threadID.String()) }},
		// 没有装配任何需要人审的本地执行器（shell/apply_patch 为空），这里仍挂一个恒拒的
		// reviewer：万一 core 的审批阶段被触发，也是 fail-closed。
		Approver: denyReviewer{},
	}, nil
}

// ensureThreadRow 给 core 直接 spawn 的线程补建 agent_threads 行（幂等）。
func (e *Engine) ensureThreadRow(ctx context.Context, threadID protocol.ThreadID, cfg core.SessionConfiguration) error {
	source := airushSessionSource()
	parent := cfg.ForkedFromThreadID
	// 子 agent：multiagent 把子线程的 SessionSource（含父线程 id）放进配置；据此登记 parent_thread_id。
	if src, ok := cfg.SessionSource.(rollout.SessionSource); ok {
		source = src
		if src.SubAgent != nil && src.SubAgent.ThreadSpawn != nil {
			p := src.SubAgent.ThreadSpawn.ParentThreadID
			parent = &p
		}
	}
	params := threadstore.CreateThreadParams{
		SessionID:      threadID.ToSessionID(),
		ThreadID:       threadID,
		Source:         source,
		HistoryMode:    protocol.ThreadHistoryModePaginated,
		ParentThreadID: parent,
		Metadata:       threadstore.ThreadPersistenceMetadata{ModelProvider: providerName},
	}
	err := e.store.Threads().CreateThread(ctx, params)
	var se *threadstore.Error
	if errors.As(err, &se) && se.Kind == threadstore.ErrorKindInvalidRequest {
		return nil // already exists
	}
	if err != nil {
		return err
	}
	return e.store.SetThreadAttributes(ctx, threadID, pgstore.ThreadAttributes{Model: cfg.Model()})
}

// providerName 是 session_meta / metadata 里的模型供应商名（平台侧只有一个供应商：LLM 网关）。
const providerName = "airush-llm"

// newModelClient 建 Responses API 客户端：指向 LLM 网关，认证与记账全部由 Meter transport
// 承担（api.NoOpAuth），模型元数据按逻辑名派生（网关侧路由到真实供应商）。
func (e *Engine) newModelClient(threadID protocol.ThreadID, cfg core.SessionConfiguration) (core.ModelClient, error) {
	slug := cfg.Model()
	if slug == "" {
		slug = e.cfg.DefaultModel
	}
	info := modelsmanager.ModelInfoFromSlug(slug)
	mc, err := core.NewResponsesModelClient(core.ModelClientConfig{
		SessionID:      threadID.ToSessionID(),
		ThreadID:       threadID,
		InstallationID: e.cfg.PodName,
		Provider: api.Provider{
			Name: providerName, BaseURL: e.cfg.LLMBaseURL,
			// codex 缺省：4 次重试（5xx/传输错误）、SSE 空闲 300s；采样级重试另在 core（D0.5）。
			Retry: api.DefaultRetryConfig(), StreamIdleTimeout: streamIdleTimeout,
		},
		Auth:      api.NoOpAuth{},
		Transport: client.NewHTTPClientTransport(&http.Client{Transport: e.cfg.LLMTransport}),
		ModelInfo: info,
	})
	if err != nil {
		return nil, fmt.Errorf("runtime: build model client for %s: %w", threadID, err)
	}
	return mc, nil
}

// newToolRouter 装配工具路由：无本地执行器；MCP 经审批门；多 agent 控制面共用 PG 图存储。
func (e *Engine) newToolRouter(threadID protocol.ThreadID) (core.ToolRouter, error) {
	deps := core.BuiltinToolDeps{}
	if e.cfg.MCP != nil {
		deps.Mcp = &gatedMcpCaller{engine: e, threadID: threadID}
		deps.McpTools = e.mcpTools
	}
	control, err := multiagent.NewControl(multiagent.Config{
		Engine:           e.tm,
		Graph:            e.store.Graph(),
		SessionID:        threadID.ToSessionID(),
		ExecutionLimiter: e.limiter,
	})
	if err != nil {
		return nil, fmt.Errorf("runtime: build collab control for %s: %w", threadID, err)
	}
	control.RegisterSessionRoot(threadID, airushSessionSource())
	deps.Collab = multiagent.NewCollabAdapter(control)
	router, err := core.BuiltinToolRouter(deps)
	if err != nil {
		return nil, fmt.Errorf("runtime: build tool router for %s: %w", threadID, err)
	}
	return router, nil
}

// staticModels 是 core.ModelsManager 的平台实现：模型元数据按逻辑名派生。
type staticModels struct{ defaultSlug string }

func (m staticModels) ModelInfo(_ context.Context, slug string) (any, error) {
	if slug == "" {
		slug = m.defaultSlug
	}
	return modelsmanager.ModelInfoFromSlug(slug), nil
}

func (m staticModels) DefaultModelSlug() string { return m.defaultSlug }

// pgRecorder 是 core.RolloutRecorder 的 PG 实现：Record = 追加到线程事件流（AppendItems 同事务
// 推进 last_seq），Flush 无操作（PG 即耐久）。写完通知本 pod 的事件订阅者。
type pgRecorder struct {
	store    *pgstore.Store
	threadID protocol.ThreadID
	notify   func()
}

func (r *pgRecorder) Record(ctx context.Context, items []rollout.RolloutItem) error {
	if err := r.store.Threads().AppendItems(ctx, threadstore.AppendThreadItemsParams{ThreadID: r.threadID, Items: items}); err != nil {
		// core 会吞掉这个错误（上游同样 log-and-continue）：这里就是唯一的可见处。
		obs.LoggerFrom(ctx).Error("rollout persistence failed", "thread_id", r.threadID.String(), "items", len(items), "error", err)
		return err
	}
	if r.notify != nil {
		r.notify()
	}
	return nil
}

func (r *pgRecorder) Flush(context.Context) error { return nil }

// denyReviewer 是 core 审批阶段的恒拒 reviewer（本部署没有需要它的工具，纯保险）。
type denyReviewer struct{}

func (denyReviewer) ReviewApproval(context.Context, *core.TurnContext, core.ApprovalAction, *string, *string) protocol.ReviewDecision {
	msg := "actions requiring approval are not available in this deployment"
	return protocol.ReviewDecision{Kind: protocol.ReviewDecisionDenied, Rejection: &msg}
}

// streamIdleTimeout 是 Responses SSE 的空闲上限（codex 缺省 300s）。
const streamIdleTimeout = 300 * time.Second
