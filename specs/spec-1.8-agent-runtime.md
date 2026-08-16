# spec-1.8 Agent Runtime 骨架（codexgo 抽核服务化 + 对齐 0.147 五块）

> **frozen** — user approve 2026-08-16（§8 Q1-Q9 **全采 ★**；`agents.default_model` 追加迁移一并批准；codexgo 传递依赖清单与 CI 二次 checkout 凭据两条硬门槛在 §9 步骤 1 落实后再单独请批）。
> 范围按 2026-08-16 user 决定：**airush 侧 runtime 骨架 + codexgo 侧全部对齐工作**（簇 D 接口 / id v7 / wait
> 失败上抛 + core 盘点五块）整合进本 spec，一份 spec、一条实施线。依据 `docs/codexgo-diff-inventory-bcd.md`、
> `docs/codexgo-diff-inventory-core.md`、`docs/agent-core-design.md`（2026-08-16 修订）。

## Header / 元数据

- **位置**：Stage 1 框架组第二件（1.7 LLM 网关 → **1.8** → 1.9 Skill 框架 → 1.18-1.20 记忆/知识库）。
  前置：spec-1.7（`libs/llm.Meter`、`AIRUSH_AGENT_LLM_*`、逻辑模型名）、spec-1.1（`agents` 表、租户中间件、
  repo 基座）、spec-0.6/0.7/0.9/0.10、`libs/tenancy`（1.7 提取）；codexgo 簇 A（MCP，spec 49 已完成）。
  被 spec-1.9（skill 调用面挂在 runtime 的 MCP client 上）、1.10-1.12（skill）、1.13/1.14（前端）、
  1.15（审计事件源）、1.18/1.19（记忆读写挂载点）、2.5（审批令牌流接本 spec 的审批阶段）消费；
- **上游决策**：**AD-11**（Agent 核心 = codexgo 抽核）、**AD-12**（skill 协议 = MCP，agent 进程内不执行命令）、
  **AD-1**（agent 无状态：会话状态全部外置）、**AD-9**（动作类须审批 + 令牌）、**AD-10**（租户隔离由 DB 强制）、
  AD-8（LLM 经网关）；`agent-core-design.md` §2 服务化架构与 §2.1 租户助理 Agent；`k8s-scaling-design.md` §2.1
  无状态排水；`memory-knowledge-architecture.md` §8-10（threadstore-PG、rollout 事件模型、上下文装配）；
  `decoupling-architecture.md` R1（AgentCore 接口）；
- **核心定位**：把 codexgo 的 agent loop 变成**多租户、无状态、k8s 可扩缩的服务**：会话与事件外置控制面 PG
  （RLS）、LLM 经 1.7 网关与 Meter、租户上下文贯穿、有服务化入口与调度器；同时在抽核分支上把 agent loop
  抽到 0.147 该有的形态（steer、上下文窗口/压缩、集中审批阶段、客户端健壮性、协议新增、线程模型接口）——
  **一步到位，避免 PG 表与事件模型二次迁移**；
- **依赖审批（规则 5 硬门槛 #4）**：**新增 Go module 依赖 = codexgo 核心包**（`go.mod replace` 指向
  `~/codexgo` 抽核分支 `airush-core`，本仓不复制代码；codexgo 已是 AD-11 既定资产，非新第三方）；
  codexgo 传递依赖随之进入 airush 的 lockfile（CI 安全扫描覆盖）——**其中若含 airush 未审的第三方直接依赖，
  在 D0 步骤 1 列清单请批**（预计：`google/uuid` 已有、`mvdan.cc/sh`（applypatch，不带走）等应被裁掉）；
  **无新增系统服务**（PG/Redis/LiteLLM 均已有）；
- **决策日期**：2026-08-16 起草并 approve（§8 全 ★）。

---

## §1 范围

### §1.1 包含（Deliverables）

| # | Deliverable | 文件清单 | 估算 | 说明 |
|---|---|---|---|---|
| **D0** | **codexgo 抽核分支 `airush-core`**（在 `~/codexgo` 仓，按其纪律：小 spec + DEVIATIONS 登记） | 见 D0.1-D0.8 | ~4.2k LOC（Go） | 抽核前把 loop 抽到 0.147 形态；下列每项在 codexgo 侧各有用例 |
| D0.1 | ThreadStore 接口对齐 0.147 + id v7 | `internal/threadstore/store.go`（+14 方法：`DeleteThread(s)`、`ArchiveThreads`、`PrepareFork`、`LoadLatestModelContext`、`SearchThreadOccurrences`、`ListItems`/`ListTurns`（补 0.136 缺项）、sections 五个、`DefaultHistoryMode`/`Supports*`）；`in_memory.go`/`local*.go` 未实现者返回 `ErrorKindUnsupported`；`protocol/ids.go` v7；删 appserver `thread-%020d` stub | ~600 | bcd 盘点 §3；接口先行、实现分期 |
| D0.2 | 输入队列 steer 准入 | `internal/core/input_queue.go`（0.147 `subscribe_activity`/steer 挂起判定/唯一触发父线程）、`user_message_admission.go`、multiagent v1 wait `Steered` 中断 | ~450 | core 盘点 ① |
| D0.3 | 上下文窗口/预算/压缩 | `internal/core/context_window.go`（`ContextWindowTokenStatus`）、`token_budget.go`、`compact_token_budget.go`（按预算开新窗口）、`compact_model_fallback.go`、`protocol/compacted_item.go`、工具 `get_context_remaining`/`new_context_window`；删 `auto_compact_window.go` 旧判定 | ~800 | core 盘点 ②；簇 E 核心 |
| D0.4 | 集中审批阶段 | `internal/core/tools/approvals.go`（`RequestApproval` → user / reviewer / 自动放行 三路 + `RecordResolution`）、`executed_tool_calls.go`；guardian **不做**（接口留 `ReviewerApprover`） | ~600 | core 盘点 ③；AD-9 agent 侧半边 |
| D0.5 | 客户端健壮性 | `internal/api`/`client`：请求项去重（忽略内部元数据）、Responses 兼容头、逐请求 reasoning effort、重试用例；`HTTPClientTransport` 接受外部 `*http.Client`（Meter 注入点） | ~300 | core 盘点 ④ |
| D0.6 | 协议新增 | `protocol`：`EventMsg` +`SubAgentActivity`/`RawResponseCompleted`/`TurnModerationMetadata`/`SafetyBuffering`；`ResponseItem` +`AgentMessage`/`AdditionalTools`；item id v7 | ~350 | core 盘点 ⑤ |
| D0.7 | multiagent 修补 | v1 `wait`：`Errored/NotFound` → `Failed` 上抛；`AgentExecutionLimiter`（按执行 turn 计并发）接口；删 `agent_jobs` | ~350 | bcd 盘点 §1 |
| D0.8 | 删除上游已放弃项 | goals（`state/goals.go` + 迁移 + 工具）、agent_jobs、`auto_compact_window` 旧判定；DEVIATIONS 记录 | −（净删） | 别把上游放弃的东西带进 airush |
| **D1** | agent-runtime 模块骨架 | `agent-runtime/cmd/agent-runtime/{main,server}.go`（替换脚手架）、`agent-runtime/internal/{runtime,api,scheduler,tenantctx}/`、`agent-runtime/go.mod`（replace → codexgo `airush-core`）、`go.work`、`.env.example`、`deploy/charts/airush/templates/agent-runtime.yaml` + values、`deploy/docker/go.Dockerfile`（COMPONENT 已支持） | ~900 | 配置面（§2.6）、健康探针、obs 三件套、Helm 组件（无状态、preStop、`terminationGracePeriodSeconds: 330`、PDB） |
| **D2** | threadstore-PG + 事件模型 | `console/migrations/0006_agent_threads.up/down.sql`；`agent-runtime/internal/pgstore/{store,threads,events,queue,graph}.go` 实现 codexgo `threadstore.ThreadStore`（D0.1 的 32 方法：Stage 1 实现 create/resume/append/persist/flush/shutdown/discard/load_history/read/list/search/update_metadata/archive/unarchive/delete/list_items/list_turns/prepare_fork/load_latest_model_context/paginated history；sections/occurrences 显式 `Unsupported`）；`agentgraph.AgentGraphStore` PG 实现 | ~1.4k | 表见 §2.3；工具结果引用化（32KB 内联上限 + 数据指针）；月分区 |
| **D3** | 租户上下文与 LLM 接线 | `agent-runtime/internal/tenantctx`（`libs/tenancy` → threadstore RLS / MCP metadata / 网关头 / 日志字段）；`ThreadServicesFactory` 装配：模型客户端 `http.Client{Transport: llm.Meter}`（1.7）、`CallInfo{AgentID,SessionID,TraceID,Purpose}`；模型逻辑名来自 agents 表 / 默认 `chat-default` | ~450 | AD-1：进程内零租户状态 |
| **D4** | 会话调度器 + 服务化入口 | `agent-runtime/internal/scheduler`（每租户并发上限来自控制面配额、每会话逐轮串行、会话间并行、steer 入队）；内部 API（svc token）`/internal/v1/agent/threads*`（§2.5）；console 公开面 `console/internal/httpapi/agent.go` 反代（默认租户中间件） | ~1.1k | 事件流 SSE；AgentCore 接口（R1）落地 |
| **D5** | 无状态排水与恢复 | preStop：停领取新 turn → 等在飞 turn（上限 5 分钟）→ 退出；启动期：按 `threads.status='running'` + 无心跳的孤儿 turn 标 `interrupted` 并可 resume（rollout 为 SSOT） | ~350 | k8s-scaling §2.1；T18-T19 |
| **D6** | 审批阶段接口（AD-9 占位） | `agent-runtime/internal/approvals`：`Approver` 接口 + Stage 1 实现 = **动作类工具一律拒绝并产生审批事件**（无令牌流）；只读工具放行 | ~200 | 令牌流与工作流在 spec-2.5；本 spec 保证"动作类不可能绕过审批阶段" |
| **D7** | 测试与验证 | codexgo 侧用例随 D0；airush：单元/集成（真 PG + 真 LiteLLM 容器 + mock 供应商）/dev-verify（runtime pod 起、经 console 发一轮对话、事件落 PG、Meter 记账、steer、排水） | ~2.0k | §4 |

合计估算 ~11.5k LOC（codexgo 侧 ~4.2k，airush 侧 ~7.3k）。

### §1.2 不包含（每条带理由）

| # | 不包含 | 理由 |
|---|---|---|
| 1 | guardian（模型自动审阅审批） | 归 spec-2.5 审批工作流"自动批准低风险"档位；本 spec 只带审批**阶段**抽象与"动作类一律拦"的 Stage 1 实现 |
| 2 | 记忆读写（Graphiti）与知识库检索 | 归 1.18/1.19/1.20；本 spec 在 `ThreadServicesFactory` 留 `MemoryClient` 注入点（nil = 不检索），上下文装配的"注入检索到的记忆"步骤留空实现 |
| 3 | skill 注册表、MCP endpoint 发现、skill 容器 | 归 spec-1.9；本 spec 的 MCP client 只接受静态 endpoint 列表（配置注入），供 1.9 换成注册表 |
| 4 | 前端对话工作台 / 巡检视图 | 归 1.13/1.14；本 spec 只给 console 公开 API + SSE |
| 5 | 审计事件模型与查询 | 归 1.15；本 spec 保证 rollout 事件含审计所需元数据（工具/审批/token/子 agent），不另建审计表 |
| 6 | 远程压缩（cloud compaction）、realtime、code_mode、unified_exec、deferred environments、图片输入、时间工具、agents.md 发现、elicitation | core 盘点 §3 ⑥"不要/以后"；均为本地 CLI 或非 Stage 1 能力 |
| 7 | 定时巡检 / 任务队列 / KEDA 伸缩 | 巡检调度归 spec-3.9；KEDA 归 1.16 验收；本 spec 的调度器只做"会话内串行、会话间并行、租户并发上限"，入口是 API 触发 |
| 8 | 冷归档 rollout 到对象存储、按租户分区滚动 | Stage 4；本 spec 只做按月分区 + 保留策略框架挂钩（spec-0.6） |
| 9 | codexgo 主线合并 | 是否把 `airush-core` 分支合回 codexgo 主线由 codexgo 纪律另定；本 spec 只要求分支可用、可 replace |

### §1.3 例外说明

- **前置变更**：原 roadmap §0.4 "P0 四簇在 codexgo 主线完成"为 1.8 硬前置；2026-08-16 user 定改为"簇 A 已完成，
  其余并入本 spec D0 在抽核分支实施"（已同步 roadmap/agent-core-design/sync-assessment）；
- **跨仓实施**：D0 在 `~/codexgo` 仓；airush 的 CI 只覆盖 airush 侧；D0 的绿灯以 codexgo 侧用例 + airush 集成用例
  （经 replace 编译并跑）双重保证。

---

## §2 接口设计

### §2.1 运行时全景

```
 console（公开 API，默认租户中间件）
   └─ /api/v1/agent/threads* ──反代(svc token)──▶ agent-runtime Pod ×N（无状态）
                                                     ├─ api        内部 HTTP + SSE
                                                     ├─ scheduler  租户并发上限 / 会话串行 / steer 入队
                                                     ├─ runtime    codexgo ThreadManager（airush-core）
                                                     │    ├─ Store = pgstore（控制面 PG，RLS）
                                                     │    ├─ ServicesFactory：LLM client（Meter→LiteLLM）、MCP client（静态 endpoints）
                                                     │    └─ approvals：动作类拦截（AD-9 占位）
                                                     └─ tenantctx  ctx 里的租户贯穿到 store/MCP/网关/日志
```

### §2.2 AgentCore 接口（decoupling R1，平台拥有）

```go
// agent-runtime/internal/runtime/core.go —— 会话调度器 ↔ agent core 的唯一接口；换 core 只换实现。
type AgentCore interface {
    StartThread(ctx context.Context, in StartThreadInput) (ThreadRef, error)          // 建线程（不发 turn）
    SubmitTurn(ctx context.Context, threadID string, in TurnInput) (TurnRef, error)   // 发起/steer 一轮
    Interrupt(ctx context.Context, threadID string) error
    Events(ctx context.Context, threadID string, fromSeq int64) (<-chan Event, error) // 从 seq 起订阅（回放 + 实时）
    ResumeThread(ctx context.Context, threadID string) error                            // pod 重建后按 rollout 恢复
}
// Event = protocol.EventMsg 的租户安全投影（不含 tenant_id 之外的其它租户信息）
```

codexgo 侧对应：`core.ThreadManager.StartThreadWithOptions/ResumeThreadByID`、`Codex.Submit/NextEvent/Shutdown`。

### §2.3 D2：schema（迁移 0006，全部租户表 + RLS 四要素）

```sql
CREATE TABLE agent_threads (
    tenant_id        uuid NOT NULL REFERENCES tenants(id),
    id               uuid NOT NULL,                        -- UUIDv7（runtime 生成）
    agent_id         uuid,                                 -- agents(id)；助理 agent 为系统内置行
    parent_thread_id uuid,                                 -- 子 agent / fork 来源
    fork_source_seq  bigint,                               -- fork 自源线程的事件序号（0.147 prepare_fork 语义）
    title            text NOT NULL DEFAULT '',
    status           text NOT NULL CHECK (status IN ('idle','running','interrupted','archived','deleted')),
    model            text NOT NULL,                        -- 逻辑模型名（spec-1.7）
    running_pod      text,                                 -- 排水/恢复用：谁在跑（AD-1：可重建，不是真相源）
    heartbeat_at     timestamptz,
    last_seq         bigint NOT NULL DEFAULT 0,
    created_at / updated_at / archived_at timestamptz,
    PRIMARY KEY (tenant_id, id),
    FOREIGN KEY (tenant_id, agent_id) REFERENCES agents(tenant_id, id),
    FOREIGN KEY (tenant_id, parent_thread_id) REFERENCES agent_threads(tenant_id, id)
);
-- rollout 事件流（event sourcing；append-only；月分区）
CREATE TABLE agent_rollout_events (
    tenant_id   uuid NOT NULL,
    thread_id   uuid NOT NULL,
    seq         bigint NOT NULL,                           -- 线程内单调
    turn_id     uuid,
    event_type  text NOT NULL,                             -- 白名单：见 §3.3（含 compacted_item / agent_message / sub_agent_activity / raw_response_completed）
    payload     jsonb NOT NULL,                            -- ≤ 32KB 内联；超出截断并写 payload_ref
    payload_ref text,                                      -- 数据指针（TimescaleDB 采集数据 / 对象存储，Stage 4）
    created_at  timestamptz NOT NULL DEFAULT now(),
    PRIMARY KEY (tenant_id, thread_id, seq, created_at)
) PARTITION BY RANGE (created_at);                        -- 按月；分区创建与保留走 spec-0.6 框架
-- 外置输入队列（steer / 排队消息，AD-1）
CREATE TABLE agent_thread_queue (
    tenant_id uuid NOT NULL, thread_id uuid NOT NULL, id uuid NOT NULL,
    kind text NOT NULL CHECK (kind IN ('steer','queued')),
    payload jsonb NOT NULL, admitted_turn_id uuid, created_at timestamptz NOT NULL DEFAULT now(),
    PRIMARY KEY (tenant_id, id)
);
-- 子 agent 拓扑（agentgraph.AgentGraphStore 的 PG 后端）
CREATE TABLE agent_graph_edges (
    tenant_id uuid NOT NULL, parent_thread_id uuid NOT NULL, child_thread_id uuid NOT NULL,
    role text NOT NULL DEFAULT '', created_at timestamptz NOT NULL DEFAULT now(),
    PRIMARY KEY (tenant_id, parent_thread_id, child_thread_id)
);
-- 索引：events (tenant_id, thread_id, seq)；threads (tenant_id, status, heartbeat_at)；queue (tenant_id, thread_id, created_at)
```

**删除语义**（0.147）：删线程 = 标 `deleted` + 级联子线程与队列；被删除集合外的子线程仍引用则拒绝（`AR_AGENT_THREAD_IN_USE`）。

### §2.4 codexgo 侧接口变更（D0，摘要；细节在 codexgo spec）

- `threadstore.ThreadStore` +14 方法（D0.1）；`ThreadIDFactory` 缺省 v7；
- `core.SessionServices` 新增：`ContextWindow`（D0.3）、`Approver`（D0.4）、`InputQueue` 后端接口（D0.2，PG 实现在 airush）；
- `client.NewHTTPClientTransport(*http.Client)`（D0.5）；
- `protocol.EventMsg`/`ResponseItem` 新变体（D0.6）；`multiagent` 失败上抛与并发限（D0.7）。

### §2.5 服务化入口（内部 API，svc token；console 反代为公开面）

| 方法 | 路径 | 语义 |
|---|---|---|
| POST | `/internal/v1/agent/threads` | `{tenant_id, agent_id?, model?, title?}` → 201 `{thread_id}` |
| POST | `/internal/v1/agent/threads/{id}/turns` | `{tenant_id, input:[…]}`；线程空闲 → 开 turn；运行中 → **steer 入队**（202 + `queued:true`）|
| POST | `/internal/v1/agent/threads/{id}/interrupt` | 中断当前 turn |
| GET | `/internal/v1/agent/threads/{id}/events?from_seq=` | SSE：先回放 ≥from_seq 的持久事件，再实时 |
| GET | `/internal/v1/agent/threads?cursor=&limit=` | 分页列表（keyset） |
| GET | `/internal/v1/agent/threads/{id}/items?cursor=` | 分页历史（0.147 `thread/items/list` 语义） |
| DELETE | `/internal/v1/agent/threads/{id}` | 删除（级联规则见 §2.3） |

公开面：`console` `/api/v1/agent/threads*` 一比一反代（默认租户中间件注入 tenant_id）；SSE 透传。

### §2.6 配置项（agent-runtime）

| 变量 | 说明 |
|---|---|
| `AIRUSH_AGENT_LISTEN_ADDR` | 缺省 `:8082` |
| `AIRUSH_AGENT_DB_URL` | 控制面 PG（同库，`SET LOCAL ROLE airush_app` 租户事务） |
| `AIRUSH_AGENT_LLM_URL` / `AIRUSH_AGENT_LLM_KEY` | 1.7 定版；key 经 Secret（`secret:"true"`） |
| `AIRUSH_AGENT_CONSOLE_URL` / `AIRUSH_AGENT_SVC_TOKEN` | 配额门与记账（`llm.ConsoleClient`） |
| `AIRUSH_AGENT_DEFAULT_MODEL` | 缺省 `chat-default` |
| `AIRUSH_AGENT_MAX_CONCURRENT_TURNS` | 单 pod 并发 turn 上限（缺省 8）；每租户上限来自控制面（Stage 1 = 该值） |
| `AIRUSH_AGENT_DRAIN_TIMEOUT` | preStop 等在飞 turn 上限，缺省 `300s` |
| `AIRUSH_AGENT_MCP_ENDPOINTS` | 静态 skill endpoint 列表（1.9 换注册表） |
| `AIRUSH_AGENT_DEFAULT_TENANT_ID` | Stage 1 |
| `AIRUSH_COMMON_*` | 观测三件套（同其它组件） |

---

## §3 行为契约

1. **无状态**：任何 pod 可接任何租户任何线程的 turn；进程内只有正在执行的 turn 的瞬时状态；pod 重建后按 rollout 重放恢复（`ResumeThread`）；
2. **租户贯穿**：每次 turn 的 ctx 必带 tenant；pgstore 所有 SQL 走租户事务；MCP 调用 metadata 与网关头带 tenant/agent/session/trace；无租户 ctx 的 turn 不启动（fail-closed）；
3. **事件白名单**：`event_type` 只接受 codexgo `protocol.EventMsg` 变体名 + `compacted_item`/`agent_message`（未知类型显式拒绝，`AR_AGENT_EVENT_UNKNOWN`）；工具结果 >32KB 截断 + `payload_ref`；
4. **steer**：运行中的线程收到新输入 → 入 `agent_thread_queue(kind=steer)` → 当前 turn 的 wait 立即中断并接纳（0.147 语义）；接纳关系写 `admitted_turn_id`；
5. **配额**：turn 开始前调度器查每租户并发上限；每次 LLM 调用经 Meter 过月度配额门（1.7）；超额 → turn 以 `AR_QUOTA_EXCEEDED` 结束并写事件；
6. **审批阶段**：动作类工具（工具目录里 `kind=action`）进入 `Approver`；Stage 1 实现 = 拒绝并写 `approval_requested`+`approval_denied` 事件；只读工具直放。**不存在绕过路径**（工具路由只认审批阶段的返回）；
7. **排水**：`SIGTERM` → 停领取 → 等在飞 turn ≤ `DRAIN_TIMEOUT` → 未完成的 turn 标 `interrupted` 并写事件 → 退出；`terminationGracePeriodSeconds: 330`；
8. **恢复**：启动期扫描 `status='running' AND heartbeat_at < now()-2×心跳` 的线程标 `interrupted`（可 resume）；不自动重跑（避免重复动作）；
9. **兼容性**：新增表/API，不动既有；`agents` 表新增列 `default_model text`（可空）——spec-1.1 已 shipped，此列变更登记于 §11 并附迁移。

---

## §4 测试用例

### codexgo 侧（随 D0，各在其包内）

| # | 用例 |
|---|---|
| C1 | ThreadStore 32 方法：in_memory/local 对未实现方法返回 `ErrorKindUnsupported`；已实现方法契约测试可被 pgstore 复用（导出 `threadstore/contracttest`） |
| C2 | id v7：线程/turn/item id 时序单调，无 `thread-%020d` |
| C3 | steer：turn 进行中提交输入 → wait 立即中断、`admitted_turn_id` 指向接纳的 turn |
| C4 | 上下文窗口：`ContextWindowTokenStatus` 触顶 → 按预算压缩 → `compacted_item` 入 rollout → resume 只重放检查点后缀 |
| C5 | 审批阶段：动作类工具 → `RequestApproval` 三路分流；`RecordResolution` 落事件 |
| C6 | 客户端：请求项去重忽略内部元数据；重试语义；reasoning effort 逐请求 |
| C7 | 协议：新变体 JSON 往返；`RawResponseCompleted` 携 usage 不累计 |
| C8 | multiagent：子 agent `Errored/NotFound` → 父 wait 得 `Failed`；并发限按执行计 |
| C9 | goals/agent_jobs 删除后编译与 parity 差分绿（DEVIATIONS 登记） |

### airush 单元 / 集成（真 PG + 真 LiteLLM 容器 + mock 供应商）

| # | 用例 |
|---|---|
| T1 | 0006 up→down→up 幂等；四表 RLS 四要素；月分区存在；跨租户不可见 |
| T2 | pgstore 通过 codexgo `contracttest` 全套（create/resume/append/history/list/search/archive/delete/prepare_fork/latest_model_context/paginated items） |
| T3 | 事件白名单：未知 event_type 拒；>32KB 工具结果截断 + `payload_ref` |
| T4 | 一轮对话经 runtime → LiteLLM（mock）→ 事件落 PG（含 `raw_response_completed`）→ Meter 记账到 console `llm_usage` |
| T5 | steer：运行中提交 → 队列表落行 → 当前 turn 接纳 → 事件可见 |
| T6 | 中断：Interrupt → turn 结束事件、线程回 `idle` |
| T7 | 租户并发上限：第 N+1 个 turn 排队不启动；跨租户互不影响 |
| T8 | 无租户 ctx → 不启动（`AR_TENANT_CONTEXT_MISSING`） |
| T9 | 审批阶段：动作类工具被拒 + 两条审批事件；只读工具直放 |
| T10 | 配额：console 配额下调至已用以下 → 下一 turn 以 `AR_QUOTA_EXCEEDED` 结束并写事件 |
| T11 | 子 agent：spawn → 子线程 `parent_thread_id` + `agent_graph_edges`；子失败 → 父得 `Failed` |
| T12 | 上下文窗口：长对话触发压缩 → `compacted_item` 事件；`ResumeThread` 只重放后缀（用事件计数断言） |
| T13 | 删除：级联子线程/队列；集合外仍被引用 → `AR_AGENT_THREAD_IN_USE` |
| T14 | 分页：`/items?cursor=` keyset 分页与总数一致；`/threads` 分页 |
| T15 | SSE：`from_seq` 回放 + 实时衔接无缝无重 |
| T16 | 排水：SIGTERM 期间在飞 turn 跑完；超时 → `interrupted` 事件 |
| T17 | 恢复：模拟 pod 死（心跳过期）→ 新实例启动把线程标 `interrupted` → 可 `ResumeThread` |
| T18 | Meter 4xx→error 契约（1.7 登记）：网关 400/429/5xx 下 codexgo 客户端的重试/展示行为符合预期，否则改透传响应 |
| T19 | 观测：`airush_agent_turns_total{status}`、`airush_agent_turn_duration_ms`、并发 gauge |

### 端到端（dev-verify）

| # | 用例 |
|---|---|
| T20 | `airush-agent-runtime` Pod ready；`helm` 幂等；securityContext |
| T21 | 经 console `/api/v1/agent/threads` 建线程 → 发一轮 → SSE 收到 `task_complete` → PG 有事件 → `llm_usage` 多一行 |
| T22 | 运行中再发一条 → 202 `queued` → 队列表有行 → 最终事件流里可见接纳 |
| T23 | `kubectl delete pod` 在飞 → 新 pod 起 → 线程 `interrupted` → 可 resume |

---

## §5 与现有代码的 contract

| 模块 | 动作 |
|---|---|
| `~/codexgo`（分支 `airush-core`） | D0 全部；按其纪律：一个 spec（暂名 spec 50 "airush-core alignment 0147"）+ DEVIATIONS `forward-synced` 登记；不改主线 |
| `agent-runtime/` | 脚手架整体替换（spec-0.1 占位） |
| `go.work` / `agent-runtime/go.mod` | `replace github.com/sqlrush/codexgo => ../codexgo`（路径按 CI 布局改为 checkout 子模块或 vendor 目录——**D1 步骤 1 定**，见 R7） |
| `console/migrations` | **新增** 0006（四表 + `agents.default_model` 列） |
| `console/internal/httpapi` | **新增** `agent.go` 反代；`svcapi` 不动 |
| `libs/llm` | 不动（1.7 契约）；`libs/tenancy` 不动 |
| `deploy/charts/airush` | **新增** agent-runtime 组件（含 Secret 引用 `airush-llm-master-key`、svc token） |
| `proto/errors.json` | **追加** `AGENT` 域：`AR_AGENT_THREAD_NOT_FOUND`、`AR_AGENT_THREAD_IN_USE`、`AR_AGENT_EVENT_UNKNOWN`、`AR_AGENT_TURN_REJECTED`（并发/配额）、`AR_AGENT_ACTION_NEEDS_APPROVAL` |
| `docs/decoupling-architecture.md` | R1 行填实：`AgentCore` 接口落地位置 |

---

## §6 风险

| # | 风险 | 概率 | 缓解 |
|---|---|---|---|
| R1 | **抽核分支与 codexgo 主线漂移**：D0 改在分支，主线继续走 parity/forward-sync | 高 | DEVIATIONS 逐项登记 forward-synced；每项对应上游 0.147 文件/行；分支 rebase 主线按 codexgo 纪律；airush 只锁分支 commit hash |
| R2 | **codexgo 传递依赖进入 airush**（未审的第三方） | 高 | D0 步骤 1 产出依赖清单；不带走的包（applypatch/sandbox/tui）在 replace 前确认不被核心包引用；未审依赖按规则 5 硬门槛 #4 请批 |
| R3 | **事件表体量**：单 turn 20-100KB，100 租户/年 2-5TB | 中 | 32KB 内联上限 + 指针；月分区；保留策略框架；Stage 1 验收实测校准 |
| R4 | **排水超时丢 turn**：5 分钟内跑不完的长 turn 被标 interrupted | 中 | rollout 为 SSOT，可 resume；`terminationGracePeriodSeconds: 330`；巡检类长任务在 3.9 走任务队列 |
| R5 | **Kimi K3 推理 item 经 Responses 桥接**：reasoning item 往返、`max_tokens` 被思考吃光 | 中 | T18/T4 用真 K3 金丝雀（key 已在 Mac）；`max_output_tokens` 缺省调高；D0.5 的请求项去重覆盖 reasoning 元数据 |
| R6 | **Meter 4xx→error 契约**与 codexgo 客户端重试逻辑不合 | 中 | T18 专项；不合则 Meter 改透传响应 + 清洗正文（1.7 §11 已登记退路） |
| R7 | **多仓构建**：airush CI 拿不到 `~/codexgo` | 高 | D1 步骤 1 定：CI 里 `actions/checkout` 第二仓到 `../codexgo` 并锁 commit，或 `go mod vendor` 进 airush（体积大）；两案在 §8 Q7 |
| R8 | **PG 单表承接读写热点**（事件 append + SSE 回放） | 中 | 回放走 `(tenant_id, thread_id, seq)` 索引；实时事件走进程内 fan-out，不轮询 PG；基线在 1.16 |
| R9 | **压缩语义在 PG 后端上的正确性**（检查点定位、重放后缀） | 中 | T12 + C4 双侧用例；`compacted_item` 是一等事件类型 |
| R10 | 估算 11.5k LOC 偏大，实施期 5-6 周 | 高 | §9 分三阶段各有可验收产物；阶段 A（codexgo）与阶段 B（PG store）可并行 |

---

## §7 DoD

- [ ] D0.1-D0.8 在 codexgo `airush-core` 分支完成，各有用例，DEVIATIONS 登记，parity 差分不红（因删 goals/agent_jobs 的差异已登记）；
- [ ] D1-D7 全部交付；0006 up→down→up 幂等；
- [ ] pgstore 通过 codexgo `threadstore/contracttest` 全套（T2）；
- [ ] AD-1：runtime 进程内无租户状态——`kubectl delete pod` 后线程可 resume（T17/T23）；
- [ ] AD-10：四表 RLS 四要素 + 跨租户用例（T1）；
- [ ] AD-9：动作类工具无绕过路径（T9），审批事件落 rollout；
- [ ] 一轮对话端到端：console → runtime → LiteLLM → 事件落 PG → `llm_usage` 记账（T4/T21）；
- [ ] steer / 中断 / 并发上限 / 配额 / 子 agent 失败上抛 / 压缩 + resume 后缀 / 删除级联 / 分页 / SSE 衔接各有用例（T5-T15）；
- [ ] Meter 4xx→error 契约在真客户端下验证或修正（T18）；
- [ ] Kimi K3 金丝雀：一轮真对话 + 一次工具调用经桥接成功（Mac 本地，key 不进仓）；
- [ ] 覆盖率：agent-runtime ≥80%、console ≥80%；CI 全绿（含第二仓 checkout 或 vendor）；
- [ ] Helm：agent-runtime 组件、preStop/330s/PDB/探针；dev-verify T20-T23 ALL PASS；
- [ ] 观测：`airush_agent_*` 三件套；
- [ ] 文档：spec 状态、roadmap §8、CHANGELOG、decoupling R1 落地、agent-core-design §1.1 按 D0 更新、`.env.example`；
- [ ] 依赖清单（codexgo 传递依赖）经 user 批。

---

## §8 Q&A（决策点）

### Q1：抽核形态？

- **★ A. `go.mod replace` 指向 codexgo 仓的 `airush-core` 分支**（本地 `../codexgo`，CI 二次 checkout 锁 commit）。
  理由：一份源码、codexgo 纪律与 parity 基建继续可用、D0 的用例在原地跑；airush 不复制代码。
- B. 复制核心包进 airush（`agent-runtime/internal/codexcore`）。零跨仓构建问题，但从此分叉，簇 A 之后的 forward-sync
  要做两遍。
- C. codexgo 打 tag 发布为 module 版本。最干净，但 codexgo 现在"分支 + 本地部署"纪律，发版链路要新建，且每次改动
  都要发版——1.8 期间迭代频繁，不合适；Stage 2 稳定后可转 C。

### Q2：服务化入口协议？

- **★ A. HTTP + SSE**（内部 svc token；console 反代为公开面）。理由：与 console/gateway 同栈；前端（1.14）直接消费 SSE；
  排障 curl 可见。
- B. gRPC 双向流。类型更强，但前端要 grpc-web 网关，多一层；Stage 1 无必要。

### Q3：事件流存储形态？

- **★ A. 单表 `agent_rollout_events` 按月 RANGE 分区 + 32KB 内联上限 + 指针**。理由：memory-knowledge §9 四层治理的前两层；
  查询模式（按线程按 seq）单一；分区滚动让热数据集不随时间线性涨。
- B. 每线程一表 / 大对象。表数爆炸或失去按事件查询能力。
- C. 放进 tsdb（TimescaleDB 超表）。事件不是读数；且要动 AD-10 等效形态（1.5 承诺唯一使用者）。

### Q4：输入队列（steer）外置到哪？

- **★ A. PG 表 `agent_thread_queue`**（与线程同库同事务）。理由：接纳关系（`admitted_turn_id`）要与事件同事务落；
  体量极小；Redis 会引入第二真相源。
- B. Redis list。延迟略低，但可丢、双源；memory-knowledge §10 把 Redis 定位为"可丢的装配缓存"，队列不该放那。

### Q5：租户上下文来源？

- **★ A. Stage 1 与 console 同：默认租户中间件注入**；内部 API 载荷带 tenant_id（gateway→console 同法）。
- B. 从 JWT 解。归 spec-2.2 认证；本 spec 的注入点保持单一，届时只换注入方。

### Q6：调度器并发模型？

- **★ A. pod 无差别 + 每租户并发上限（Stage 1 = 单 pod 上限配置）+ 会话内串行/会话间并行**。理由：k8s-scaling §2.4a
  "agent 是逻辑身份不是算力单元"；上限来源留控制面接口，2.8 接配额。
- B. 每租户/每 agent 固定 pod。与 AD-1 相悖，浪费。

### Q7：CI 如何拿到 codexgo？

- **★ A. GitHub Actions 二次 checkout `sqlrush/codexgo`（`airush-core` 分支，锁 commit）到 `../codexgo`**，`go.work`
  相对路径不变。理由：本地与 CI 布局一致；不膨胀 airush 仓。前提：CI token 能读 codexgo（私有仓 → deploy key/PAT，
  D1 步骤 1 确认）。
- B. `go mod vendor` 进 airush。CI 自包含，但每次 D0 改动都要重 vendor 提交，仓体积 +数万行。

### Q8：压缩范围？

- **★ A. 本地压缩全带（含按预算开新窗口、模型回退），远程压缩不带**。理由：远程压缩依赖 OpenAI 后端服务；
  本地压缩是长会话正确性前提且与 PG 事件模型（`compacted_item`）绑定。
- B. 全不带、留 1.10+。会让 1.8 的事件模型没有压缩 item，簇 D 高效 resume 落空，后补要改 schema。

### Q9：审批阶段的 Stage 1 实现？

- **★ A. 动作类工具一律拒 + 写审批事件；只读放行**。理由：AD-9 的"不可能绕过"从第一天就成立；令牌流与人审在 2.5。
- B. 不带审批阶段，2.5 再加。届时要在工具路由里"插一刀"，且 1.8-2.5 之间存在动作类可被调用的窗口（哪怕
  Connector 侧会拒）——两道门只剩一道。

---

## §9 实施计划

| 阶段 | # | 步骤 | 估时 |
|---|---|---|---|
| **A codexgo** | 1 | 建 `airush-core` 分支 + codexgo 侧 spec（DEVIATIONS 形态）+ **传递依赖清单**（R2，请批）+ CI 二次 checkout 可行性（Q7） | 1 天 |
| | 2 | D0.1 ThreadStore 对齐 + `contracttest` 导出 + id v7 + 删 stub | 3 天 |
| | 3 | D0.6 协议新增 + D0.5 客户端 + D0.7 multiagent 修补 + D0.8 删除 | 3 天 |
| | 4 | D0.2 steer 准入 + D0.3 上下文窗口/压缩 + D0.4 审批阶段 | 5 天 |
| **B airush store** | 5 | 0006 迁移 + pgstore（跑 contracttest）+ 事件白名单/截断 + T1-T3 | 4 天 |
| | 6 | D1 骨架 + D3 租户/LLM 接线 + T4/T8/T18（真 LiteLLM 容器 + Kimi 金丝雀） | 3 天 |
| **C 服务化** | 7 | D4 调度器 + 内部 API + SSE + console 反代 + T5-T7/T10-T11/T13-T15 | 5 天 |
| | 8 | D6 审批阶段接线 + T9；D5 排水/恢复 + T16-T17 | 3 天 |
| | 9 | Helm + dev-verify T20-T23 + 覆盖率 + review + 文档 | 3 天 |

总计 **~30 工作日（6 周）**；阶段 A 步骤 2-4 与阶段 B 步骤 5 可部分并行（contracttest 先出）。

> 步骤 1 把"传递依赖清单"和"CI 二次 checkout"放第一天：前者是规则 5 硬门槛，后者是 R7——两者任一不成立，
> 形态就要退到 Q1-B/Q7-B，越早知道越好。

---

## §10 后续 spec 关联

- **spec-1.9**（Skill 框架）：把 `AIRUSH_AGENT_MCP_ENDPOINTS` 换成注册表发现；skill 容器 = MCP server；调用票据；
- **spec-1.10-1.12**（skill）：经 1.9 挂到本 runtime；诊断对话（1.12）用本 spec 的 steer 与 SSE；
- **spec-1.13/1.14**（前端）：消费 `/api/v1/agent/threads*` + SSE；`SubAgentActivity` 是并发巡检进度源；
- **spec-1.15**（审计）：从 `agent_rollout_events` 取工具/审批/token/子 agent 事件；
- **spec-1.18/1.19**（记忆）：`ThreadServicesFactory` 的 `MemoryClient` 注入点；上下文装配"注入检索到的记忆"步骤；
- **spec-2.5/2.6**（审批工作流/受控执行器）：接本 spec `Approver` 接口，加令牌流；guardian 作候选；
- **spec-2.8**：租户并发上限接控制面配额；
- **spec-3.9**：定时巡检走任务队列 + KEDA，复用调度器；
- **codexgo spec 50**：D0 在 codexgo 侧的正式载体。
