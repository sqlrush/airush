# codexgo 上游同步评估（codex 0.136.0 → 0.147.0）

> 日期：2026-08-08 · 状态：**策略 C 已定（user approve 2026-08-09）**——定向同步进
> codexgo 主线再抽核；同步工作在 codexgo 仓按其 spec/parity 纪律执行
> 背景：AD-11 确定 Agent 核心基于 codexgo 抽核。codexgo 锚定 codex rust-v0.136.0
> （2026-06-01 发布）；上游最新 rust-v0.147.0（2026-08-07 发布），差 11 个 minor 版本。
> 本文盘点两个月上游演进，按对 AIRush 的相关性分级，给出同步策略建议。

## 1. 高相关功能簇（建议同步）

### 簇 A：MCP 协议演进（P0——skill 协议就是 MCP）

- **MCP 2026-07-28 协议支持**（0.147）：分页工具发现、多轮请求、server 非阻塞启动；MCP SDK 升级 3.0.0；
- MCP 工具搜索默认启用（0.143）；schema 保留 oneOf/allOf、大 schema 压缩保形（0.139）；
- 可靠性包：启动超时、瞬时失败重试、OAuth 凭据序列化刷新、工具目录安全复用、
  配置变更时只重连失效 server 不重启健康连接（0.140/0.145/0.146）；
- 启动前暴露缓存工具目录（0.147）。

**AIRush 价值**：skill 容器 = MCP server（AD-12），这批直接决定 skill 调用面的协议版本与可靠性。

### 簇 B：多 agent v2 稳定化（P0——并发巡检引擎）

- 线程级 runtime 选择、agent 驻留 LRU、按活跃执行计并发（0.137/0.139）；
- **终态子 agent 错误上抛给父 agent**（0.142 #28375，此前失败会被当成空成功——对巡检正确性关键）；
- 委派模式可配置：disabled / explicit-request-only / proactive（0.142）；
- 子 agent 模型、推理档位、并发度可配置；typed envelope 消息（0.142/0.145）；
- steer 输入立即中断 wait_agent（0.141）。

**AIRush 价值**：fleet 并发巡检（MULTI-DB 设计）的执行引擎直接建立在 multiagent 之上。

### 簇 C：Token 预算与成本控制（P0——SaaS 商业模型依赖）

- **可配置 rollout token 预算**：跨 agent 线程累计、余量提醒、耗尽即中止 turn（0.142）；
- 模型自有预算默认值（0.147）。

**AIRush 价值**：按租户配额计费（AD-8）在 agent 层的执行机制，不同步就要自研。

### 簇 D：线程/会话模型升级（P0——threadstore→PG 改造的设计蓝本）

- 分页线程历史 + 高效 resume + 搜索 + 持久命名 + 子 agent 关联 + memories（0.145 实验）；
- UUID7 线程/turn ID（0.143）；按指定 turn fork、临时 fork 不入列表（0.143/0.146）；
- 列直接子线程/后代线程（0.141/0.143）；thread/delete 级联清理子 agent（0.140）；
- 编辑历史 prompt 自动开上下文分支，保原会话（0.145）；增量线程历史变更事件（0.142）。

**AIRush 价值**：会话管理是 agent 四大职责之一；换 PG 后端时按新模型设计一步到位，避免二次迁移。

### 簇 E：上下文压缩（P1——长会话与巡检报告场景）

- 远程压缩重写超大工具输出（0.138）；冷活跃 rollout 压缩（0.142）；
- 压缩引用已退役模型时自动用当前模型重试（0.144）；压缩效率优化（0.145）。

### 簇 F：安全强化（P1，Stage 2 前必须）

- **展示命令与回放历史中的 secret/bearer token 脱敏**（0.147）；
- CLI 与 MCP OAuth 凭据本地加密存储（0.140）——Connector 凭据保管（AD-4）直接参考；
- memories 排除外部工具输出（0.139）——防止客户数据渗入记忆；
- 插件隔离强化、策略更新失败时拒绝网络（0.147）。

### 簇 G：设计参考（不移植代码，吸收设计）

- **状态库自愈**（0.140）：SQLite 损坏自动备份并从 rollout 重建——确立"rollout 为 SSOT、
  state 可重建"原则，AIRush PG 后端沿用此架构原则；
- **Noise 加密中继信道**（0.141 远程执行）——Connector 反向隧道（AD-2）的安全设计参考；
- 系统代理 PAC/WPAD 支持（0.143/0.146）——企业客户内网 Connector 经代理出网的需求预研；
- 定时 UTC 提醒与当前时间查询工具（0.142）——巡检调度的 agent 侧能力参考。

## 2. 低相关（不同步）

TUI/渲染/键位全部；Desktop 交接与安装器；ConPTY/平台修复；ChatGPT 账户与用量积分体系；
/import（Claude Code/Cursor 迁移）；语音/realtime 音频；插件 marketplace 商店机制
（AIRush 自有 skill 注册表）；Bedrock 登录与模型目录（LLM 走 LiteLLM 网关）；
remote-control 配对；web search 模式（P2 观察）。

## 3. 同步策略选项

| 策略 | 内容 | 工作量 | 评价 |
|---|---|---|---|
| A. 全量 parity 升级到 0.147 再抽核 | codexgo 主线按既有 parity 纪律追平 11 个版本全部面 | 3-4 个月+ | 大量 TUI/桌面/账户工作与 AIRush 无关，拖死节奏 |
| B. 定向同步进 AIRush fork | 抽核时只把簇 A-F 移植进 airush 的 agent-runtime，codexgo 主线不动 | 与抽核合并 ~8-12 周 | 最快；代价是 codexgo 主线不受益，两边分叉 |
| C. 定向同步进 codexgo 主线再抽核（★推荐） | 簇 A-F 按 codexgo 既有 spec/parity 纪律进主线（核心包，不含 TUI 面），airush 抽核继承 | ~10-13 周（含抽核） | codexgo 资产保值、差分基建持续可用、AIRush 与 codexgo 不分叉；比 B 慢约 1-2 周 |

**推荐 C**：多花的 1-2 周买到的是"上游能力持续移植管道"的长期保留——这正是 AD-11 选
codexgo 的核心理由之一。若时间压力极大，可 B/C 混合：簇 A/C/D 走 codexgo 主线
（协议与数据模型，两边都要），簇 B/E/F 直接进 airush fork。

## 4. 建议的同步优先级与排期（策略 C 口径）

| 优先级 | 内容 | 估时 | 排期位置 |
|---|---|---|---|
| P0 | 簇 A（MCP 2026-07-28 + SDK 3.0）| 1-2 周 | 抽核前置，Stage 1 spec-1.9 依赖 |
| P0 | 簇 D（线程模型设计采纳，UUID7/分页/fork 语义）| 2-3 周 | 与 threadstore→PG 改造合并做 |
| P0 | 簇 B（multiagent v2 稳定化）| 2-3 周 | spec-1.8 依赖 |
| P0 | 簇 C（token 预算）| 1 周 | spec-1.7/1.8 依赖 |
| P1 | 簇 E（压缩）+ 簇 F（安全脱敏/加密凭据）| 2-3 周 | Stage 1 后半 / Stage 2 前 |
| — | 簇 G（设计吸收）| 随相关 spec | 写进对应设计文档 |

## 5. 修订历史

| 日期 | 变更 |
|---|---|
| 2026-08-08 | 初版：0.137-0.147 release notes 全量盘点与分级 |
| 2026-08-16 | **进度与源码校正**：簇 A 已按 codexgo spec 49 前向同步到 0.147（需求 1-5，v0.5.0）；簇 B/C/D 按源码逐簇盘点见 `docs/codexgo-diff-inventory-bcd.md`——三处校正：① 线程/turn id 的 UUID v7 在 **0.136 已有**（`protocol/thread_id.rs`），codexgo 的 v4/stub 是移植偏差而非版本差异；② 簇 C 的"模型自有预算默认值（0.147）"实为上下文窗口预算 + 自动压缩回退，语义偏簇 E；③ 簇 D 上游 thread-store 增量的一半以上是本地 JSONL 文件工程（写锁/迁移/压缩/反向扫描），PG 后端不需要抽，故"D 2-3 周"可下修为"接口与语义对齐 ~1 周"。§4 路线建议：只补 D 的接口 + B 的失败上抛 + id v7（codexgo 侧 ~1 周），C 走 airush 侧 |
| 2026-08-16 | **core/protocol/client 盘点**见 `docs/codexgo-diff-inventory-core.md`：core +1.4 万行里"带走"范围内值得吸收约 4 千行——① 输入队列/steer 准入 ② 上下文窗口预算与压缩（簇 E 核心，含 `compacted_item`）③ 集中审批阶段 + guardian 自动审阅（AD-9 agent 侧）④ 客户端去重/重试 ⑤ 协议新增 `SubAgentActivity`/`RawResponseCompleted`/`AgentMessage`；上游已删 goals/agent_jobs，codexgo 应同步删。合并 bcd 结论：codexgo 侧抽核前小 spec 约 2-2.5 周 |
