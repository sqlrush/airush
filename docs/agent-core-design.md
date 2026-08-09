# Agent 核心设计：codexgo 抽核服务化

> 日期：2026-08-09 · 状态：设计定稿，随 spec-1.8 细化 · 上游决策：AD-11、AD-12
> 同步升级策略：**策略 C**——6 个功能簇定向同步进 codexgo 主线（按其 spec/parity 纪律），
> airush 抽核继承。盘点见 `docs/codexgo-sync-assessment.md`。

## 1. 抽核范围

### 1.1 带走的包（codexgo → airush agent-runtime）

| codexgo 包 | 用途 | 改造 |
|---|---|---|
| core / protocol / api / client | agent 循环、事件协议、模型 API 客户端 | LLM 端点指向 LiteLLM 网关（OpenAI 兼容），去除本地登录态 |
| mcp | MCP client | 升级 MCP 2026-07-28（同步簇 A）；连接池按 skill endpoint 管理 |
| multiagent | 并发子 agent 引擎 | 继承 v2 稳定化（同步簇 B）；并发度接租户配额 |
| threadstore / state / rollout | 会话与状态存储 | **新增 PG 后端实现**（接口已抽象，现有 in_memory/local 双实现为证）；线程模型按同步簇 D 设计（UUID7、分页、fork 语义）；沿用"rollout 为 SSOT、state 可重建"自愈原则 |
| config（子集） | 配置加载 | 改为平台配置中心/环境变量来源，去除 ~/.codex 本地文件 |
| modelproviderinfo / modelsmanager | 模型目录 | 精简为网关可用模型目录 |
| otel | 可观测性 | 对接 spec-0.9 三件套 |

### 1.2 不带走

tui、sandbox（三平台）、applypatch、exec/execserver/execpolicy、pty、shellcmd、
login/keyring（CLI 态）、appserver 的桌面协议面、filesearch/filewatcher/gitutils、
realtime\*、cloud\*、chatgpt、analytics。理由：编码场景/本地 CLI 专属，AIRush 的
skill 不在 agent 进程内执行任何命令（AD-3/AD-12）。

### 1.3 替换实现

| codexgo 机制 | AIRush 替换 |
|---|---|
| memories（本地文件记忆） | 记忆服务 client（HTTP API → Graphiti，见 storage-selection.md） |
| SQLite state DB | 控制面 PG（RLS 多租户） |
| 本地 rollout JSONL | PG 大对象/分表存储，冷数据归档对象存储（Stage 4） |
| ~/.codex 配置 | 控制面 agent 配置实体（AD-1） |

## 2. 服务化架构

```
                    ┌─ agent-runtime Pod（无状态，水平扩容）─┐
 任务入口            │ ┌────────────┐   ┌───────────────┐  │
 ├ 控制台会话 ──────▶│ │ 会话调度器   │──▶│ codexgo core   │  │
 ├ 巡检调度(Cron) ──▶│ │ (租户上下文  │   │ agent loop     │  │
 └ 事件触发 ────────▶│ │  + 配额检查) │   │ + multiagent   │  │
                    │ └────────────┘   └──┬────┬────┬───┘  │
                    └────────────────────┼────┼────┼──────┘
                          threadstore-PG ┘    │    └ MCP → skill 服务
                          context → Redis     └ LLM → LiteLLM 网关
                          记忆 → 记忆服务 API
```

- **租户上下文**：每个 turn 携带 `tenant_id / agent_id / session_id / trace_id`，
  从入口注入，贯穿 threadstore 查询（RLS）、MCP 调用 metadata、LLM 网关计费头、日志字段；
- **无状态回收**：会话状态全部外置，pod 可随时回收；preStop 等待处理中 turn 完成（上限 5 分钟）；
- **配额**：token 预算（同步簇 C）按租户配置，耗尽中止 turn 并产生可审计事件。

## 3. 抽核实施顺序（对应 spec-1.8 拆解）

1. codexgo 主线完成同步簇 A（MCP）与簇 D（线程模型）——P0 前置；
2. airush 内建 `agent-runtime/` 模块，vendor codexgo 核心包（go.mod replace 指向抽核分支）；
3. threadstore PG 后端 + 租户上下文注入（TDD：先写多租户隔离测试）；
4. 会话调度器 + 服务化入口（HTTP/gRPC）；
5. LLM 网关对接与 token 预算；
6. multiagent 并发巡检通路（依赖簇 B）。

## 4. 修订历史

| 日期 | 变更 |
|---|---|
| 2026-08-09 | 初版（AD-11/AD-12 落地设计，同步策略 C 确认后） |
