# codex → codexgo → airush 能力继承清单

> 日期：2026-08-11 · 状态：初版（随 spec-1.8 抽核实施细化）
> 上游决策依据：`docs/2026-08-08-airush-platform-design.md` AD-11（Agent 核心=codexgo 抽核）、
> AD-12（skill 协议=MCP）；同步策略：`docs/codexgo-sync-assessment.md`（策略 C）。
>
> **本文用途**：作为 spec-1.8（Agent Runtime 抽核服务化）"到底 vendor 哪些包"的 SSOT——
> 抽核时按本清单取舍，避免漏搬（缺能力）或误搬（把不需要的单机/UI/账户栈拖进平台）。

## 0. 三层坐标系

```
原生 codex 0.147（Rust，全能力）
   │  codexgo：Go 移植，几乎全量搬了 codex 的【单机产品】能力（含 TUI/账户/沙箱）
   ▼
codexgo（单机 Go 产品，锚 rust-v0.136.0，MCP 面追平 0.147=spec 49）
   │  airush：只抽 codexgo 的【内核】做多租户服务化（AD-11）
   ▼
airush agent-runtime（无状态服务，注入租户上下文，存储外置 PG/Redis/Graphiti）
```

「airush 继承什么」的权威边界是 **AD-11**：复用 `core / protocol / mcp / multiagent /
threadstore` 五大内核，换存储后端为 PG/Redis、注入租户上下文、服务化运行；保留 codexgo
差分基建以持续从 codex 上游移植（`docs/codexgo-sync-assessment.md`）。其余能力要么被平台
架构**替换**，要么**丢弃**。

## 1. ✅ 继承（AD-11 抽核的五大内核 + 配套）

| codex 能力 | codexgo 包 | airush 用法 | 关联同步簇 |
|---|---|---|---|
| Agent 推理循环（turn 执行/采样/工具编排） | `core` | agent-runtime 引擎；去 TUI 事件、服务化调度 | — |
| 协议/事件模型（ResponseEvent、submission/op） | `protocol` | 跨服务事件契约基础 | — |
| MCP 客户端（连外部 MCP server、工具发现/调用） | `mcp` | **skill 调用协议（AD-12）**——skill=MCP server | 簇 A（追平 0.147） |
| 多 Agent（spawn 子 agent、委派、agent 图） | `multiagent`/`agentgraph` | **fleet 并发巡检引擎** | 簇 B |
| 线程/会话模型（rollout/fork/resume/历史） | `threadstore`/`rollout` | 会话管理；**存储后端换 PG**（见 §2） | 簇 D |
| 工具框架（function tool、tool-search、并行工具） | `tools` | agent 工具调用载体 | — |
| Token 预算（跨线程累计、耗尽中止） | (core turn) | 按租户配额在 agent 层执行 | 簇 C |
| 上下文压缩（auto-compact、长会话） | (core) | 长巡检/诊断会话 | 簇 E（P1） |

## 2. 🔄 继承概念，但换后端 / 换边界（非原样搬）

| codex 做法 | airush 替换为 | 依据 |
|---|---|---|
| 会话/状态存 SQLite（`state`/`thread-store`） | **PG（控制面）+ Redis（上下文）** | AD-1 进程无状态 / AD-10 RLS |
| 模型客户端直连供应商（`model-provider`/`ollama`/`lmstudio`/Bedrock） | **LLM 网关（LiteLLM）** 统一路由/配额/降级 | AD-8 |
| 记忆（`memories`，本地） | **Graphiti + Neo4j**（时序知识图谱） | AD-5/AD-6 |
| 单机凭据/登录（`keyring`/`login`，`~/.codexgo`） | **多租户认证 + RLS + secret 管理** | AD-4/AD-10，spec-2.2 |
| 本地工具执行（见 §3.A） | **Connector 受控执行器 + 审批 + 双层白名单** | AD-9 |

> 说明：codexgo 为对接非 OpenAI 后端（GLM/DeepSeek）自带 chat-completions wire API 扩展
> （见 codexgo `DEVIATIONS.md`）；airush 的模型接入统一收口到 LLM 网关，该扩展的价值由网关承接。

## 3. ❌ 不继承（平台架构不需要，明确丢弃）

### A. 本地代码 / shell 执行栈（codex 是编码 agent，airush 是 DB 运维 agent）
- `apply-patch`（改代码）、`exec`/`shell-command`/`unified-exec`/`exec-server`（本地 shell）、
  `code-mode`（代码执行模式）、`execpolicy`（本地审批）
- `sandboxing`/`linux-sandbox`/`bwrap`/`windows-sandbox`/`process-hardening`（本地沙箱）
- → 全被 **AD-9 Connector 受控执行器**取代：动作走客户侧代理 + 一次性令牌 + 双层白名单，
  平台侧不执行任意 SQL / 任意 shell（spec-1.2 已落地指令通道骨架）

### B. 终端 UI / 交互层（airush 是 Web 平台，React 前端 + 无头 agent 服务）
- `tui`、`ansi-escape`、`terminal-detection`、`pty`
- `realtimeconv`/`realtimewebrtc`（语音/实时音频）
- Desktop/IDE 交接、ConPTY、`@show`/relevance-gate 类 TUI 渲染

### C. 账户 / 云 / 商业体系（airush 有自己的控制面与商业模型）
- `chatgpt`（ChatGPT 账户）、`cloud`/`cloudreq`（云任务）、`backend-client`、用量积分体系
- `login`（单用户登录）、`analytics`/`feedback`（上游遥测）、`install-context`
- Bedrock/`aws-auth` 登录

### D. 生态迁移 / 插件市场（airush 自有 skill 注册表，spec-1.9）
- 插件 marketplace 商店机制、`/import`（Cursor/Claude Code 迁移）、`external-agent-migration`
- `plugins`/`hooks`（codex 插件体系）——airush skill 走 MCP，不用这套

### E. 其他不相关
- `network-proxy`/PAC/WPAD（企业代理，仅作 Connector 出网**设计参考**，不移植代码）
- web search 模式（P2 观察）、`v8-poc`、`remote-control` 配对

## 4. ⏳ 追平到 0.147 的范围（spec 49 簇 A，仅针对已继承的 MCP 内核）

codexgo 锚 rust-v0.136.0，MCP 客户端是继承的核心，需追到 0.147：
- 需求 1 协议版本协商（2026-07-28 → 2025-06-18 降级）；
- 需求 2 非阻塞启动 + 工具目录**内存 LRU** 缓存（源码核实：`Arc<Mutex<LruCache>>`，容量 32/
  TTL 30min，**非磁盘**——初稿"磁盘+JSON"表述已校正）；
- 需求 3 schema 保形（oneOf/allOf）；需求 4 连接可靠性（启动超时/退避重试/选择性重连）；
- 需求 5 OAuth token 刷新（序列化写回，不做完整授权流）。

**需求 6（工具暴露策略）已建议摘出簇 A**：codexgo 现走 eager 暴露（`ListAllToolInfos`），
上游 0.147 走 always-defer-behind-tool-search，而 codexgo 在建自己的 `@show`/relevance-gate
路线——这是路线抉择（§8 维护者决策），与协议同步正交，待方向定后单独立项。

## 5. 修订历史

| 日期 | 变更 |
|---|---|
| 2026-08-11 | 初版：基于 codex 0.147 crate 清单 + AD-11/AD-12 + codexgo-sync-assessment 策略 C 整理 |
