# codexgo 上游差异盘点：0.136 → 0.147，`core` / `protocol` / `client`（抽核主体）

> 日期：2026-08-16 · 性质：**只读盘点，不改任何代码** · 用途：回答"spec-1.8 抽核抽的 agent loop 是不是过时语义、要从新版本加哪些"。
> 参考树与方法同 `codexgo-diff-inventory-bcd.md`（基线 rust-v0.136.0，目标 rust-v0.147.0，codexgo 现状 feat/opendb-mvp v0.5.0；非测试行数；`diff -rq` + 函数名集合差 + 关键文件精读）。
> 分类口径沿用 2026-08-16 user 认可的"带走 / 替换 / 不要"表（`agent-core-design.md` §1）。

## §0 一页结论

- 上游 `core/src` 非测试代码 **8.2 万 → 9.7 万行（+1.4 万）**；`protocol` 事件 74 → 80 个变体，`ResponseItem` 15 → 17。
- 这 1.4 万行里，**属"带走"范围且值得从新版本吸收的约 4 千行**，集中在 5 块：① 输入队列 / steer 准入；② 上下文窗口预算与压缩（簇 E）；③ **集中审批阶段 + guardian 自动审阅**（AD-9 的 agent 侧半边）；④ 模型客户端健壮性（重试、元数据、请求项去重）；⑤ 事件/协议新增（子 agent 活动、原始 usage 事件、agent 消息）。
- 另有约 4 千行落在"不要"范围（deferred environments、realtime、code_mode、unified_exec、图片准备），**不抽**。
- 上游**删除**了 goals（1854 行）与 agent_jobs（~1100 行）——codexgo 都移植过（`internal/state/goals.go`、`agent_jobs.go`），**抽核前应同步删除**，别把上游已放弃的东西带进 airush。
- **codexgo 现状**：`internal/core` 2.15 万行，是 0.136 core 的子集移植；已有 approvals（0.136 形态）、auto compact、事件压缩，**无** steer/输入队列新语义、无 context_window/token_budget、无 guardian、无集中审批阶段、无 SubAgentActivity/RawResponseCompleted 事件。
- **结论**：抽核不是"过时到不能用"，0.136 的 agent loop 骨架仍成立；但 5 块新增里 ①②③ 直接对应 airush 的三个需求（对话工作台 steer、长巡检会话、AD-9 审批），**建议纳入 1.8 的抽核范围（按需求 4 千行的量级，1-1.5 周）**，其余在 §3 逐项标"不要/以后"。

## §1 结构变化总览（core/src，非测试行数，变化 ≥150 行）

| 区域 | 0.136 | 0.147 | 归类 | 说明 |
|---|---|---|---|---|
| `session/` | 10464 | 14157 (+3693) | **带走** | agent loop 本体：新增 `input_queue` steer 语义、`context_window`/`token_budget`、`world_state`、`step_context`、`mcp_prewarm/refresh/runtime`（簇 A）、`rollout_budget`（簇 C）、`time_reminder` |
| `tools/` | 21852 | 24257 (+2405) | 框架带走 / 执行面不要 | 新增 `approvals.rs`（集中审批阶段 503 行）、`executed_tool_calls.rs`（314）、`router` 延迟工具命名空间；handlers 新增 `get_context_remaining`/`new_context_window`/`current_time`/`sleep`/`wait_for_environment`；**删** `agent_jobs`、`goal` |
| `context/` | 2324 | 3990 (+1666) | 带走 | 上下文片段体系化：`fragment(s).rs`、`world_state/`、`prompts/`；新增 skill/协作模式/多 agent 模式/预算/时间提醒/agent 间消息等片段 |
| `config/` | 7661 | 9009 (+1348) | 子集带走 | 新配置项：`rollout_budget`、`current_time_reminder`、多 agent 版本/模式、环境选择、code_mode 等；airush 只取与 loop 相关的子集 |
| `environment_selection.rs` | 213 | 1281 (+1068) | **不要** | 本地/云 deferred environment 选择与等待（`wait_for_environment` 工具、`EnvironmentConnected/Disconnected` 事件） |
| `agent/` | 2120 | 3158 (+1038) | 带走 | 已在簇 B 盘点：驻留 LRU、执行并发限、spawn 拆分、失败上抛 |
| `realtime_conversation*` | 1543 | 2465 (+922) | 不要 | 实时语音 |
| `guardian/` | 4305 | 5023 (+718) | **带走（改造）** | "guardian review 决定 on-request 审批是否可**自动**通过而不打扰用户"：策略模板 + 审阅会话 + 快照——AD-9 的自动审阅层，见 §3 ③ |
| `thread_manager.rs` | 1571 | 2050 (+479) | 带走 | `fork_prepared_thread`、`load_latest_model_context`（簇 D）、agent_graph_store 接线、MCP runtime 失效（簇 A）、多 agent 版本决策 |
| `unified_exec/` | 2566 | 3048 (+482) | 不要 | 本地进程执行 |
| `responses_metadata.rs` | 0 | 411 | 带走（小） | app-server 客户端可随请求传 `responsesapi_client_metadata`——airush 里等价物是 Meter 注入的头 + LiteLLM metadata，可借鉴其字段白名单思路 |
| `image_preparation.rs` | 0 | 279 | 以后 | 图片输入尺寸/格式准备；Stage 1 无图片输入 |
| `client.rs` | 2252 | 2451 (+199) | 带走 | 见 §3 ④ |
| `compact*.rs` | 1344 | 1876 (+532) | 本地压缩带走 / 远程不要 | 新增 `compact_token_budget`（按 token 预算手动压缩、开新窗口）、`compact_model_fallback`（压缩失败换模型重试）、远程压缩拆文件（remote 不要） |
| `goals.rs` + `tools/handlers/goal*` | 1854 + 158 | **删除** | — | 持久化线程目标功能整体移除 |
| `tools/handlers/agent_jobs*` | ~1100 | **删除** | — | CSV 批量 spawn 移除 |
| `landlock.rs` / `shell_detect.rs` / `personality_migration.rs` / `review_*.rs` | 小 | 删除/迁移 | 不要 | 沙箱/CLI 周边 |

## §2 协议面（`protocol` crate）

| 项 | 0.136 → 0.147 | 对 airush |
|---|---|---|
| `EventMsg` 变体 | 74 → 80：**+`SubAgentActivity`**（子 agent 活动流）、**+`RawResponseCompleted`**（"与 TokenCountEvent 不同，不累计/不估算/不重放"的原始 usage 事件）、`TurnModerationMetadata`、`SafetyBuffering`（流式安全缓冲）、`EnvironmentConnected/Disconnected`（不要） | 前两个直接影响 airush：**子 agent 活动**是控制台展示并发巡检进度的数据源（1.13/1.14）；**RawResponseCompleted** 是把 turn 级 usage 写进 rollout 事件的正确挂点（memory-knowledge-architecture §8.2"每 turn token 用量"），与 1.7 传输层 Meter 互为印证 |
| `ResponseItem` 变体 | 15 → 17：**+`AgentMessage`**（agent 间消息作为一等 item）、`AdditionalTools` | `AgentMessage` 进 rollout 事件模型（1.8 的 `rollout_events.event_type` 白名单要含它） |
| 新文件 | `capabilities.rs`、`compacted_item.rs`（压缩产物 item）、`legacy_events.rs`、`response_item_id.rs`（item id 改 v7）、`models/` 拆目录、`review_format.rs`（从 core 迁入） | `compacted_item` 与簇 D "检查点可定位"直接相关：**压缩事件必须是 rollout 里可识别的 item**，高效 resume 靠它 |
| 线程/turn id | 两版都 v7；0.147 把 item id 也 v7 | codexgo 全部 v4 + stub → 抽核时统一 v7（bcd 盘点 §3.2） |

## §3 逐块判断（"带走"范围内，要不要从新版本加）

### ① 输入队列与 steer 准入 —— **加**

- 位置：`session/input_queue.rs`（337→483，`subscribe_mailbox` → `subscribe_activity`，新增 steer 订阅/已挂起 steer 判定/唯一触发父线程约束）、`user_message_admission.rs`（"接受该用户消息的 turn"）、v2 `wait` 的 `WaitOutcome::Steered`。
- 语义：用户在 turn 进行中再发消息 → 进入队列并**立即打断**正在等待子 agent 的 wait，由当前 turn 或下一 turn 接纳，接纳关系可追溯。
- airush：通用对话工作台（1.14）"诊断中途补一句"就是 steer；k8s 无状态 pod 下队列**必须外置**（PG `queued_items` 或 Redis，bcd 盘点 §3.2 已列）。**1.8 应按 0.147 语义设计队列表**。~500 行 Rust → Go 估 400 行。

### ② 上下文窗口预算与压缩（簇 E 的核心）—— **加**

- 位置：`session/context_window.rs`（`ContextWindowTokenStatus`：活跃上下文 token、自动压缩范围与上限、完整窗口上限、是否触顶）、`session/token_budget.rs`、`compact_token_budget.rs`（"按 token 预算的手动压缩走正常压缩生命周期，跳过模型摘要，直接装一个新窗口"）、`compact_model_fallback.rs`、`protocol/compacted_item.rs`；工具 `get_context_remaining`、`new_context_window`（模型可自查余量、自开新窗口）；`ModelTokenBudgetConfig`（模型级阈值提醒 + 自动压缩回退提示）。
- 0.136：只有 `auto_compact_token_status` 一处（0.147 删了它，换成上面这套）；codexgo 有 `auto_compact_window.go`/`event_compact.go`（0.136 形态）。
- airush：巡检会话 1-3 MB、诊断会话多轮——**上下文管理是长会话正确性的前提**（memory-knowledge-architecture §9/§10 已把"压缩事件 + 检查点"写进 rollout 设计）。1.8 若不带，等于 PG 事件模型里没有压缩 item，簇 D 的高效 resume 也落空。~700 行。

### ③ 集中审批阶段 + guardian 自动审阅 —— **加（改造）**

- 位置：`tools/approvals.rs`（0.147 新，503 行：`request_approval` → 按策略分流 `request_user_approval` / `request_reviewer_approval` / `request_guardian_approval`，`record_resolution`；0.136 这段散在 `orchestrator.rs` 的 `request_approval/reject_if_not_approved`）、`guardian/`（4305→5023：guardian 是"on-request 审批能否自动通过"的审阅模型会话，带策略模板 `policy.md`、快照、指标）、`tools/executed_tool_calls.rs`（会话级已尝试工具元数据，供取消/压缩/审计）。
- 上游语义：工具调用先过**审批阶段**，策略决定"直接放行 / 交人 / 交审阅者 / 交 guardian 模型自动审"，结果记录。
- airush：**这就是 AD-9 在 agent 进程内的半边**——动作类工具调用（经 Connector 受控执行器）必须先过审批 + 一次性令牌。上游的"审批阶段"抽象直接可用；guardian 的"模型自动审阅低风险请求"是 Stage 2 spec-2.5 审批工作流里"自动批准低风险"档位的现成设计。**1.8 带走审批阶段抽象（~500 行），guardian 作为 2.5 的候选实现登记，不在 1.8 实现**。codexgo 现有 `approvals.go`/`approval_waiters.go` 是 0.136 形态，需按 0.147 重排。

### ④ 模型客户端健壮性 —— **加（小）**

- 位置：`client.rs`（+199）：`prepare_response_items_for_request` + `response_items_equal_ignoring_internal_metadata`（请求项去重、忽略内部元数据）、`build_responses_compatibility_headers` / `add_responses_lite_header`（Responses 兼容头）、`turn_state`、`reasoning_effort_for_request`、`responses_retry_tests`（重试语义有专门用例）；删掉的 `*_window_generation`、`subagent_header_value` 等被新形态取代。
- airush：经 LiteLLM 时兼容头多数无害；**去重与重试语义值得带**（Kimi K3 是推理模型，Responses 请求里 reasoning item 的处理正是这段代码管的）。~200 行。
- 另：spec-1.7 review 登记的"Meter 把 4xx 转 Go error 对 codexgo 客户端的影响"就在这个包验证。

### ⑤ 事件/协议新增 —— **加（协议层零成本，语义随 ①②③ 落地）**

- `SubAgentActivity`、`RawResponseCompleted`、`AgentMessage`、`compacted_item` 进 rollout 事件白名单与 PG 事件模型（见 §2）。

### ⑥ 不要 / 以后（"带走"范围内但 airush 不需要）

| 项 | 判断 |
|---|---|
| `time_reminder` / `current_time` / `sleep` 工具、`CurrentTimeReminderConfig` | 以后：agent 需要"现在几点"时用一个 MCP 工具即可，不必进 loop |
| `image_preparation` | 以后：Stage 1 无图片输入 |
| `responses_metadata`（411） | 不要代码，借鉴字段白名单思路 |
| `agents_md_manager` | 不要代码：airush 的"指令文档"来自控制面（AD-1），不是文件发现 |
| `elicitation.rs`（MCP 服务端向用户提问、暂停工具结果投递） | 以后：skill 需要澄清时用；Stage 2 与审批一起看 |
| `mcp_prewarm/refresh/runtime` | 簇 A 范畴，codexgo 已同步 |
| `code_mode_warning`、`extension_metrics`、`environment_*`、`realtime_*`、`unified_exec`、`wait_for_environment` | 不要 |

## §4 codexgo 侧要同步删除的（上游已放弃）

| 项 | codexgo 位置 | 说明 |
|---|---|---|
| goals（持久化线程目标 + `create_goal/update_goal` 工具） | `internal/state/goals.go`、`goals_migrations/`、`internal/core` 相关 | 上游 0.147 整体删除；抽核前删，避免带进 airush 的 PG schema |
| agent_jobs（CSV 批量 spawn） | `internal/state/agent_job.go`、multiagent 相关 | 同上（bcd 盘点 §1.1 已列） |
| `auto_compact_token_status` 一类 0.136 形态的压缩判定 | `internal/core/auto_compact_window.go` | 被 ② 的 context_window/token_budget 体系取代 |

## §5 对 spec-1.8 的建议（合并 bcd 盘点）

**codexgo 侧（抽核前，一个小 spec，约 2-2.5 周）**：
1. `ThreadStore` 接口对齐 0.147（32 方法，未实现者显式不支持）+ 全部 id 改 v7 + v1 wait 失败上抛（bcd 盘点 §5，~1 周）；
2. 本文 ①②③④：steer 准入、上下文窗口/预算/压缩 item、集中审批阶段、客户端去重与重试（~1-1.5 周）；
3. 删 goals / agent_jobs / 旧压缩判定。

**airush 侧（1.8 本体）**：PG threadstore/rollout（事件白名单含 `AgentMessage`/`compacted_item`/`SubAgentActivity`/`RawResponseCompleted`）、外置输入队列、会话调度器、审批阶段接 AD-9 令牌流（实现在 2.5，1.8 留接口）。

**明确不做**：deferred environments、realtime、code_mode、unified_exec、guardian 实现（2.5 候选）、image、time 工具、agents.md 发现。

## §6 局限

- `session/session.rs`、`turn.rs`（2190→2740）只做了函数名集合差 + 抽样精读（新增 `build_extension_turn_input_items`、`prepare_tool_recommendations`、`required_mcp_servers_for_input`、`assign_missing_streamed_response_item_id`、`capture_current_model_fallback_step_context`），未逐行；进入 codexgo spec 时按其模板做步骤 1 的逐文件映射。
- guardian 的策略模板与 AD-9 双层白名单的对应关系未展开——归 spec-2.5。
- 未盘 `protocol` 之外的 `app-server-protocol`（airush 自建服务化入口，只借鉴 thread/* 方法语义，bcd 盘点 §3.1 已列增删）。

## §7 修订历史

| 日期 | 变更 |
|---|---|
| 2026-08-16 | 初版（user 指示"先盘下 core，看下要增加新版本的哪些内容"） |
