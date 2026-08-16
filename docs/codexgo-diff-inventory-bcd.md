# codexgo 上游差异盘点：0.136 → 0.147，簇 B / C / D

> 日期：2026-08-16 · 性质：**只读盘点，不改任何代码** · 用途：spec-1.8 抽核前置的决策依据
> （codexgo 要不要先补簇 B/C/D 再抽核，见 §5）。
> 参考树：基线 `~/codexgo/reference-codex`（rust-v0.136.0）、目标 `~/codexgo/reference-codex-0147`（rust-v0.147.0）、
> codexgo 现状 `~/codexgo/internal`（分支 feat/opendb-mvp，v0.5.0）。
> 行数均为**非测试** `.rs`/`.go` 行数（`find … ! -name '*test*'`），量级用，不是精确工作量。
> 方法：目录级 `diff -rq` 定位新增/变更文件 → 读关键文件定语义 → grep codexgo 对照。
> 簇的定义沿用 `codexgo-sync-assessment.md`（2026-08-08 由发布说明整理），本文以**源码为准**校正其中几处版本归属。

## §0 一页结论

| 簇 | 上游 0.136→0.147 变化量 | 变化本质 | codexgo 现状（相对 0.136） | 对 spec-1.8 的相关性 |
|---|---|---|---|---|
| **B 多 agent v2** | `core/src/agent` 2120→3158 行（+49%）；v2 工具集 8 个换 2 个 | 控制面拆模块（驻留 LRU、执行并发限、spawn 独立）+ 委派模式（显式/主动）+ steer 中断 wait + 子 agent 失败上抛为 `Failed` + `agent_jobs`（CSV 批量）整体移除 | 已按 0.136 移植控制面（2056 行）；**只有 v1 工具集**（spawn/wait/send_input/resume/close），无 v2 的 assign/followup/interrupt/list/send_message，无委派模式，无 steer | **中**：1.8 的会话调度器与并发巡检要 spawn/wait + 并发上限 + 失败上抛；委派模式/steer 是 TUI 交互增强，1.8 不需要 |
| **C token 预算** | 新增 191 行（`rollout_budget.rs` + session/context 各一小块）+ 配置项 4 个 | 每**根线程会话树**一份加权 token 账本；阈值提醒注入上下文；耗尽 → `SessionBudgetExceeded` 中止 turn；模型级 `ModelTokenBudgetConfig`（0.147）是上下文窗口预算 + 自动压缩回退，**偏簇 E** | **无** | **低**：租户级硬顶已由 spec-1.7 `Meter` 在传输层实现（AR_QUOTA_EXCEEDED）；簇 C 是会话级软限（提醒 + 优雅中止），锦上添花，可在 airush 侧 ~150 行实现 |
| **D 线程模型** | `thread-store` 7742→17725 行（**+129%**）；trait 新增 **14 个方法**；`rollout` +52%；`state` +14% | 分页历史（`thread_history*`、`items/list`）、高效 resume（`load_latest_model_context` 反向扫描）、按 turn fork + 血统索引（`prepare_fork`/`rollout_lineage`/`rollout_reference_index`）、删线程（含子引用保护）、线程分区（sections）、搜索命中、写锁、rollout 压缩/维护、SQLite 自愈（`state/recovery.rs`）、持久化 steer 队列（`queue_store`）| 部分移植 0.136（3685 行 vs 上游 7742）：接口 **15 方法**（缺 0.136 的 `list_items/list_turns`）；线程 id 在 appserver 是 `thread-%020d` **stub**、item id 用 uuid **v4**——**这是 codexgo 相对 0.136 的偏差，不是 0.136→0.147 差异**（上游 0.136 已是 v7） | **高**：1.8 的 threadstore→PG 就是对着这个 trait 写。取 0.136 的 15 方法接口 = 一开始就缺分页/fork/删除/高效 resume，PG 表设计会缺列缺索引，**后改 schema 最贵** |

**一句话**：B 是"控制面重构 + 交互增强"，1.8 只需其中并发上限与失败上抛；C 已被 1.7 覆盖大半；**D 是唯一直接决定 PG 表设计的簇，也是唯一"抽错了后面最贵"的簇**。

## §1 簇 B：多 agent v2

### 1.1 结构变化

| 文件 | 0.136 | 0.147 | 说明 |
|---|---|---|---|
| `core/src/agent/control.rs` | 1315 行单文件 | 804 行 + `control/{residency,execution,spawn,legacy}.rs` | 控制面拆四块：**驻留**（`V2Residency`：VecDeque LRU + pending slot，`effective_agent_max_threads(V2)` 为容量）、**执行并发**（`AgentExecutionLimiter`：按"正在执行 turn 的 agent 数"计，不按线程数）、spawn 独立、legacy 兼容 |
| `core/src/tools/handlers/multi_agents_v2/` | assign_task / close_agent / list_agents / message_tool / send_message / spawn / wait | followup_task / interrupt_agent / list_agents / message_tool / send_message / spawn / wait | 工具名：`assign_task`→`followup_task`，新增 `interrupt_agent`；`close_agent` 从 v2 目录消失但仍在 spec 工具名列表 |
| `core/src/tools/handlers/agent_jobs*` | 764+311+… 行 | **移除** | CSV 批量 spawn（`spawn_agents_on_csv`）与 job 结果上报整体删除 |
| `core/src/context/multi_agent_mode_instructions.rs`、`world_state/multi_agent_mode.rs` | 无 | 新增 | 委派模式 `MultiAgentMode::{ExplicitRequestOnly, Proactive}`，以 developer message 注入并可在会话中切换 |
| `core/src/tools/handlers/multi_agents/wait.rs`（v1） | 269 行 | 324 行 | 引入 `CodexErrorDetails::ThreadNotFound`、`AgentStatus::Errored | NotFound → CollabAgentToolCallStatus::Failed`——**子 agent 终态错误上抛为 Failed**（0.136 会当成空成功） |
| v2 `wait.rs` | 159 行 | 196 行 | `WaitOutcome::{MailboxActivity, Steered, TimedOut}`，`InputQueueActivity::Steer` 立即中断 wait |
| v2 `spawn.rs` | 320 行 | 267 行 | `model` / `reasoning_effort` 参数两版都有；0.147 走 `V2ResidencySlot` 预留槽位 |

### 1.2 行为项对照

| 评估文档条目 | 源码证据（0.147） | 0.136 有？ | codexgo 有？ |
|---|---|---|---|
| 线程级 runtime 选择、agent 驻留 LRU、按活跃执行计并发 | `control/residency.rs`、`control/execution.rs` | 无（单文件、无驻留概念） | `control.go/registry.go` 有 max threads 类配置项，无驻留 LRU、无按执行计数 |
| 终态子 agent 错误上抛给父 agent | v1 `wait.rs` `Errored|NotFound → Failed` | 无 | `status.go` 有 `AgentStatusErrored` 映射；`collab_control.go`/`control.go` 的 wait 路径 grep 无 `Failed` 上抛逻辑——与 0.136 一致，**不上抛**（待 codexgo spec 里以用例确认） |
| 委派模式可配置 disabled / explicit-request-only / proactive | `context/multi_agent_mode_instructions.rs` | 无 | 无 |
| 子 agent 模型、推理档位、并发度可配置 | v2 `spawn.rs` `model`/`reasoning_effort`；并发见 residency | 模型/推理档位有；并发无 | 需查 spawn 参数（v1 工具集） |
| typed envelope 消息 | `message_tool.rs` 两版都在（143→132 行），本次未逐行比对 | 有雏形 | 有 `send_input`，无 `send_message`/`list_agents` |
| steer 输入立即中断 wait_agent | v2 `wait.rs` `WaitOutcome::Steered` | 无 | 无 |

### 1.3 对 airush 的意义

- 1.8 的并发巡检引擎需要的是：**spawn/wait 可用、并发上限、子 agent 失败必须上抛**（否则巡检"成功"但结论是空的——正确性问题）。这三条里前两条 codexgo 已有雏形，第三条 ~50 行修补。
- 委派模式、steer 中断、typed envelope、`interrupt_agent`/`followup_task` 是交互式 CLI 场景的增强，airush 的 agent 由调度器驱动，**Stage 1 不需要**。
- `agent_jobs`（CSV 批量）上游已删，codexgo 若移植过应同步删除。

## §2 簇 C：token 预算

### 2.1 上游 0.147 实现（0.136 无）

- `core/src/rollout_budget.rs`（127 行）：`RolloutBudget` 挂在 `agent_control` 上，**每根线程会话树一份**（子 agent 共享）。`record_usage(&TokenUsage) -> bool`：加权 = `output_tokens × sampling_token_weight + non_cached_input × prefill_token_weight`（或服务端给的 `codex_rollout_budget_units`），返回 true 即耗尽。
- `core/src/session/rollout_budget.rs`：`maybe_record_reminder` 在 `remaining ≤ reminder_at_remaining_tokens[i]` 时把 `RolloutBudgetContext{remaining_tokens}` 作为 `ContextualUserFragment` 注入对话（每线程按窗口去重）；`record_rollout_budget_usage` 耗尽 → `CodexErr::SessionBudgetExceeded`（turn 中止，可审计）。
- 配置：`features.rollout_budget.{limit_tokens, reminder_at_remaining_tokens[], sampling_token_weight, prefill_token_weight}`，加载期校验（必填/正数/阈值小于上限）。
- 模型级 `ModelTokenBudgetConfig`（`protocol/openai_models.rs`，0.147）：`reminder_threshold_tokens`、提醒模板、指导语、**自动压缩回退**——这是上下文窗口层的预算，属簇 E（压缩）的范畴，评估文档把它归到 C 是按发布说明字面。

### 2.2 与 spec-1.7 的关系

| 层 | spec-1.7 Meter（已 shipped） | 簇 C RolloutBudget |
|---|---|---|
| 粒度 | 租户月度 | 根线程会话树 |
| 位置 | 传输层 RoundTripper，调用前门 + 调用后记账 | agent loop 内，每次 `response.completed` 后 |
| 超额行为 | 拒绝下一次调用（`AR_QUOTA_EXCEEDED`），当前调用不中断 | 注入余量提醒 → 耗尽时 turn 以 `SessionBudgetExceeded` 优雅中止 |
| 数据 | 控制面 PG，精确 token | 进程内加权计数，不持久 |

结论：**互补不重叠**。1.8 若要"单次巡检任务花费上限"（防一个失控 agent 烧光租户配额），簇 C 的语义正好；实现量小（~150 行 Go：账本 + 提醒注入 + 中止），**可在 airush 侧做，不必等 codexgo**。

## §3 簇 D：线程/会话模型

### 3.1 `ThreadStore` trait 面

| | 0.136 | 0.147 | codexgo |
|---|---|---|---|
| 方法数 | 18 | **32** | 15 |
| 只在 0.147 | — | `archive_threads`（批量）、`delete_thread` / `delete_threads`、`create/delete/rename/list_thread_section(s)`、`move_thread_to_section`、`default_history_mode`、`supports_paginated_history_lists`、`supports_thread_sections`、`load_latest_model_context`、`prepare_fork`、`search_thread_occurrences` | — |
| codexgo 相对 0.136 缺 | — | — | `list_items`、`list_turns`、`as_any` |

### 3.2 语义要点（决定 PG 表设计的部分）

| 能力 | 0.147 实现位置 | 语义 | PG 后端的含义 |
|---|---|---|---|
| **分页历史** | `local/thread_history.rs`、`thread_history_materialization.rs`；app-server `thread/items/list`（替代 0.136 的 `thread/turns/items/list`） | 按 turn/item 游标分页取历史，可物化 | `rollout_events` 需 `(thread_id, seq)` 主键 + turn_id 索引；API 层游标分页 |
| **高效 resume** | `local/model_context.rs::load_latest_model_context` | 反向扫描找最近的"替换历史检查点 + 已完成用户 turn"，只重放最新后缀 | 需要 **压缩/检查点事件**在事件流里可定位（事件类型列 + 检查点标记）——与 memory-knowledge-architecture §10"重放 + 应用压缩事件"一致 |
| **按 turn fork + 血统** | `local/paginated_fork.rs::prepare`、`rollout_lineage.rs`、`rollout/rollout_reference_index.rs` | fork 前先持久化源线程，解析血统（source segment），fork 引用被索引 | `threads` 需 `parent_thread_id` + `fork_source_turn`（或 seq）；删除时要检查被引用（见下） |
| **删除** | `local/delete_thread.rs` | 删除集合内的子引用一起删；集合外仍引用该线程的子线程会阻止删除 | 外键 + 引用计数或阻止规则；与 `agent_graph` 边一致 |
| **线程分区（sections）** | `thread_sections.rs` + `state/runtime/thread_section(_order)s.rs` | 用户手工排序的持久分组 | 控制台"会话列表分组"——**属 UI 便利，1.8 可不做**；表上留 `section_id` 可选列即可 |
| **搜索命中** | `search_threads.rs`、`search_thread_occurrences` | 全文命中定位到 turn | PG `tsvector`/pg_trgm 走 GIN，比 JSONL 扫描天然强 |
| **持久 steer 队列** | `queue_store.rs`、`state/runtime/queued_items.rs` | 线程内待处理用户消息持久化 | 一张小表或 Redis（k8s 无状态 pod 下**必须外置**） |
| 写锁 / 迁移 / 压缩 / 维护 / 反向扫描 | `writer_lock.rs`、`rollout_migration*`、`rollout/{compression,maintenance,reverse_jsonl_scanner}.rs` | 本地 JSONL 文件形态的工程 | **PG 后端不需要**（事务 + 索引替代）——这是 0.147 thread-store 增量里的大头，可整体不抽 |
| SQLite 自愈 | `state/runtime/recovery.rs` | 备份 + 从 rollout 重建 | 对应"rollout 为 SSOT、state 可重建"原则，PG 形态下 = 派生表可由事件表重建 |
| ID | `protocol/thread_id.rs`、`session_id.rs`：**0.136 已 `Uuid::now_v7()`**；0.147 把 item id 也改 v7（`items.rs`、`response_item_id.rs`） | 时序有序 id | codexgo `uuid.NewString()`（v4）+ appserver `thread-%020d` stub 是**移植偏差**，与版本无关；airush 生成 v7 即可（`google/uuid` v1.6.0 **已有** `NewV7`，本次核实：`version7.go`；此前记录的"无 NewV7"有误） |

### 3.3 对 airush 的意义

- 1.8 的 threadstore→PG **应对着 0.147 的 32 方法 trait 设计表**（至少覆盖分页、fork 血统、删除引用规则、检查点定位、steer 队列外置），即使首版只实现其中 1.8 用得到的子集——**表结构一步到位，接口可以分期实现**。
- 0.147 thread-store 增量的一半以上是本地 JSONL 文件的工程（写锁、迁移、压缩、反向扫描），PG 形态下**不需要抽**，所以"D 要 2-3 周"的估算可以下修：**语义采纳 + 接口对齐约 1 周，PG 实现本就在 1.8 内**。

## §4 三条路线的成本对比（修正后）

| 路线 | 内容 | codexgo 侧投入 | 1.8 延后 | 风险 |
|---|---|---|---|---|
| **A** 先补 B/C/D 再抽核 | 按策略 C 原意，三簇进 codexgo 主线（含 parity 差分） | B ~2 周（含 v2 工具集）+ C ~3 天 + D ~2 周（含本地形态工程）≈ **4-5 周** | 4-5 周 | 大量本地 JSONL 形态工作对 airush 无用；codexgo 主线受益但 airush 等 |
| **B** 按现状抽核，B/C/D 在 airush 侧按需实现 | 只取设计，代码在 airush 落地 | 0 | 0 | codexgo 与 airush 分叉（策略 B 的代价）；D 若不先对齐接口，PG 表设计按 0.136 的 15 方法走，**后改最贵** |
| **C（★）** 只在 codexgo 补 D 的**接口与语义**（不做本地文件工程），B 的失败上抛小修，C 走 airush 侧 | codexgo：`ThreadStore` 接口对齐 0.147 的 32 方法（未实现的显式返回不支持）+ id 改 v7 + v1 wait 失败上抛；airush：PG 后端 + 会话级预算 | **~1 周** | ~1 周 | 接口先行、实现分期，codexgo 主线不分叉；本地实现只补 airush 需要的方法 |

## §5 建议

**取 C**：codexgo 侧一周内做三件事——① `ThreadStore` 接口对齐 0.147（32 方法，未实现者返回 `ErrUnsupported` 而非缺席）；② 线程/turn/item id 改 UUID v7，去掉 appserver stub；③ v1 `wait` 对 `Errored/NotFound` 子 agent 上抛 `Failed`。这三件按 codexgo 纪律各写一个小 spec（或并成一个 "spec 50 thread-model-alignment-0147"）。spec-1.8 与之并行起草，其 §1.3 例外说明写明"抽核以 codexgo spec 50 完成为前置，B 的其余项与 C 不前置"。

**不建议 A**：4-5 周里超过一半在做 PG 形态用不上的本地文件工程。**不建议裸 B**：D 的接口不先定，1.8 的迁移 0006 会照 15 方法接口设计，分页/fork/删除三处后补都是改 schema。

## §6 未覆盖与已知局限

- typed envelope（B）、编辑历史自动分支 / memories 实验（D，0.145）、增量历史变更事件（D，0.142）三项**未逐行比对**（app-server v2 通知名 grep 未命中，可能以不同形态存在）；不影响 §5 结论，因为它们都是交互层能力。
- 版本归属：评估文档按发布说明标的 0.14x 小版本，本文只区分"0.136 有 / 0.147 有"，不逐版本考证。
- 工作量估算是量级判断（按非测试行数与语义复杂度），不是排期承诺；进入 codexgo spec 时按其模板重估。

## §7 修订历史

| 日期 | 变更 |
|---|---|
| 2026-08-16 | 初版（user 指示"把 0.136→0.147 在 B/C/D 三簇的差异盘点做出来"） |
