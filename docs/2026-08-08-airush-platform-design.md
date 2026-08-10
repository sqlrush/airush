# AIRush 数据库管理智能体平台 — 总体设计

> 状态：**user approved 2026-08-08**（brainstorming 会话收敛）
> 本文档是平台架构的 Single Source of Truth。后续 spec 与本文冲突时，以本文为准；
> 需要偏离本文的设计变更，必须先修订本文并获得 user approve。

## 0. 产品定位

面向企业客户的 **SaaS 多租户数据库管理智能体平台**。核心场景：

1. **运维管理**：备份恢复、监控告警、故障诊断、性能优化、容量规划的智能化；
2. **巡检与合规**：周期性健康巡检、安全合规检查、慢查询审计。

支持的数据库引擎（按协议族抽象）：

| 协议族 | 覆盖引擎 | 接入方式 |
|---|---|---|
| MySQL 协议 | MySQL / MariaDB / TiDB / OceanBase(MySQL 模式) | 通用 MySQL 驱动 + 引擎特有巡检包 |
| PostgreSQL 协议 | PostgreSQL / openGauss / GaussDB | 通用 PG 驱动 + 引擎特有巡检包 |
| 专有协议 | 达梦 | 专用驱动 |

## 1. 总体架构

```
客户内网                          SaaS 平台 (k8s)
┌──────────────┐   outbound    ┌─────────────────────────────────────┐
│  Connector    │──mTLS 长连──▶│ Connector 接入网关                    │
│  ├ 采集探针    │               │   ↓ 结构化数据                       │
│  ├ 凭据保管    │               │ 数据接入层 → TimescaleDB/元数据库      │
│  └ 受控执行器  │◀──指令下发────│                                     │
└──┬───┬───┬──┘               │ 控制台(React 前端 + Go API)           │
   │   │   │                   │ 控制面 PostgreSQL (RLS 多租户)        │
 MySQL PG 达梦                 │ Agent Runtime (Go, codexgo 核)        │
                               │   ├ 会话/上下文 → PG + Redis         │
                               │   ├ 记忆 → 记忆服务(Graphiti+Neo4j)   │
                               │   └ MCP → Skill 服务 (无状态)         │
                               │ LLM 网关 → DeepSeek/Qwen/GLM/Claude  │
                               │ 知识库(图谱+pgvector)  审批/审计服务    │
                               └─────────────────────────────────────┘
```

### 1.1 核心架构决策（brainstorming 收敛结果）

| # | 决策 | 选择 | 备选与否决理由 |
|---|---|---|---|
| AD-1 | Agent 状态模型 | **逻辑有状态、进程无状态**：agent 是控制面里的配置实体，由共享无状态 Runtime 池执行，状态全部外置（PG/Redis/记忆库） | StatefulSet 常驻进程方案否决：升级丢状态、无法水平扩容、故障转移复杂 |
| AD-2 | 连接客户数据库 | **双模式**（2026-08-10 修订）：① 客户侧 Connector 反向隧道（outbound-only mTLS 长连接，跨网/SaaS 场景）；② **平台直连**——平台与数据库同内网（本地部署 k8s）时直接连库采集/执行，免装 Connector | 公网白名单直连仍否决：直连模式仅限同内网，不经公网暴露数据库 |
| AD-3 | Skill 执行位置 | **分层混合**：Connector 内置固定采集探针上报结构化数据；分析类 skill 全部在平台侧、不碰数据库不碰凭据；实时诊断走受控隧道 | 平台侧全量执行否决：客户原始数据流经平台，合规硬伤；客户侧全量执行否决：skill 分发升级复杂、安装门槛高 |
| AD-4 | 凭据边界 | **分模式**（2026-08-10 修订）：Connector 模式凭据只存客户侧（本地加密），平台永不持有；直连模式凭据由平台 secret 管理**加密存储**（k8s Secret/KMS 信封加密），任何通道禁明文（入库/日志/LLM prompt） | 跨网 SaaS 场景平台集中保管仍否决：单点爆炸半径覆盖全部客户数据库；直连模式限本地部署（平台本就在客户域内，爆炸半径不跨客户） |
| AD-5 | 记忆库选型 | **Graphiti + Neo4j**（时序知识图谱，Neo4j 自带向量索引，图+向量一套） | Mem0 备选（保留迁移可能）；纯 pgvector 否决为终态（丢失时序因果），但作为 MVP 过渡允许 |
| AD-6 | 知识库与记忆库基础设施 | **合并**：共用 Neo4j 图谱（按租户+用途命名空间隔离）；纯文档 RAG 用控制面 pgvector | 独立两套图+向量否决：运维成本翻倍且能力重合 |
| AD-7 | 指标时序存储 | **TimescaleDB**（PG 插件，与控制面同栈） | VictoriaMetrics 留作规模化备选 |
| AD-8 | LLM 接入 | **独立 LLM 网关**（LiteLLM 起步）：多供应商、按租户配额计费、模型路由、缓存降级 | agent 直连供应商否决：成本与限流失控 |
| AD-9 | 动作类操作安全 | **审批工作流 + 一次性令牌 + 双层白名单**（平台定义操作类型、客户侧 Connector 配置允许范围）；只读操作自动执行全量审计 | 无审批直接执行否决：AI 对生产库操作必须有人工门 |
| AD-10 | 多租户隔离 | 控制面 PG **Row-Level Security** 强制 tenant_id；记忆/知识按租户命名空间；每 Connector 独立 mTLS 证书；大客户可升级独立 Runtime 池 | 应用层过滤否决：一处遗漏即数据串租 |
| AD-11 | Agent 核心 | **基于 ~/codexgo 抽核改造**（Go）：复用 core/protocol/mcp/multiagent/threadstore，换存储后端为 PG/Redis、注入租户上下文、服务化运行；保留差分基建以持续移植 codex 上游能力。详见 `docs/agent-core-design.md` | codex 原生否决：引入 Rust 栈 + 935K 行陌生代码深度 fork；opencode 否决：Node 服务端进生产栈；开源框架（LangGraph 等）否决：框架锁定且多租户服务化工作量相同 |
| AD-12 | Skill 调用协议 | **MCP（streamable HTTP）**：skill 容器 = 无状态 MCP server，工具发现/schema/调用走 MCP 标准；替换早期 gRPC 设想 | 自定义 gRPC 否决：需自建工具发现与 schema 机制，且与 codexgo MCP client 资产脱节 |

## 2. 组件职责

### 2.1 Connector（Go，单二进制）

部署在客户内网，三个职责，连接方向永远 outbound：

- **采集探针**：周期采集性能指标、慢日志、元数据、巡检项；客户可配置脱敏规则；只上报结构化数据；
- **凭据保管**：数据库凭据本地加密存储，平台永不持有；
- **受控执行器**：只接受预定义操作类型白名单指令；动作类指令必须携带平台审批服务签发的一次性令牌。

直连模式（AD-2②，本地部署）下，以上采集/执行职责由平台侧**直连接入器**承担——与
Connector 同一代码库的内嵌运行形态，探针与白名单执行逻辑复用；凭据走 AD-4 直连模式
的平台加密保管。

### 2.2 控制台（React + TypeScript 前端 / Go API）

租户与用户管理、数据源接入、agent 配置（绑定数据库、挂载 skill、巡检策略）、
巡检报告与诊断会话界面、审批与审计视图。

### 2.3 Agent Runtime（Go，codexgo 抽核，无状态 Deployment 池）

基于 codexgo 核心包（core/protocol/mcp/multiagent/threadstore）服务化改造（AD-11）。
按租户上下文加载 agent 配置执行：会话管理（threadstore→PG 后端）、上下文管理（Redis）、
记忆读写（经记忆服务 API）、知识检索（图谱 + pgvector）、skill 编排（MCP client）、LLM 网关调用。
抽核方案详见 `docs/agent-core-design.md`。

### 2.4 Skill 服务（无状态 MCP server，语言无关，Python 为主）

输入结构化数据、输出结论，不碰数据库、不碰凭据。skill 容器 = MCP server（AD-12），
经 `tools/list` 自声明 schema，被多个 agent 并发调用。两种形态：

- **常驻型**（Deployment + MCP streamable HTTP）：高频分析类（慢查询分析、巡检报告、基线对比）；
- **Job 型**（k8s Job/CronJob）：低频重活（月度合规报告、大规模扫描）。

Skill 注册表存控制面（记录 MCP endpoint 与租户可见性）；agent 挂载 skill 即配置关联。
跨容器调用细节见 `docs/skill-runtime-design.md`。

### 2.6 记忆服务（Python，包装 Graphiti）

独立服务包装 Graphiti+Neo4j，对 Agent Runtime 暴露与实现无关的记忆 API
（写入/检索/遗忘），是 AD-5 的解耦落点（换记忆库 = 换此服务实现）。

### 2.5 存储矩阵

| 存储 | 用途 | 引入阶段 |
|---|---|---|
| PostgreSQL（控制面） | 租户/用户/agent/skill 注册表/审批/审计/会话，RLS 多租户 | Stage 0 |
| TimescaleDB（PG 插件） | 采集指标时序数据 | Stage 1 |
| pgvector（PG 插件） | 文档 RAG（runbook、产品手册） | Stage 1 |
| Redis | 会话上下文缓存、任务队列 | Stage 1 |
| Neo4j + Graphiti | agent 时序记忆 + 运维知识图谱 | Stage 3 |

## 3. 技术栈

| 层 | 选型 | 理由 |
|---|---|---|
| Connector / 控制面 API / 接入网关 | Go | 单二进制交付、高并发长连接 |
| Agent Runtime | Go（codexgo 抽核，AD-11） | 自有资产、栈统一、MCP 原生 |
| Skill 服务 | MCP server，Python 为主（协议语言无关） | 分析与 LLM 生态；性能敏感 skill 可用 Go |
| 记忆服务 | Python（Graphiti） | Graphiti 官方栈 |
| LLM 网关 | LiteLLM（Python，现成） | 多供应商、配额计费开箱即用 |
| 控制台前端 | React + TypeScript | 生态成熟 |
| 编排 | k8s + Helm | 既定要求；本地开发用 kind |
| LLM | DeepSeek / Qwen / GLM / Claude（经网关） | 国内 SaaS 合规 + 能力分层路由 |

## 4. 安全模型

1. **数据边界**：客户原始数据默认不出内网；上报数据经客户可配置的脱敏规则；
   实时诊断查询作为受控例外（隧道 + 审计 + 脱敏网关）。
2. **凭据边界**：见 AD-4。平台侧 secret（LLM key、内部服务凭据）用 Vault/k8s Secret 管理，
   永不入代码、镜像、日志。
3. **操作边界**：见 AD-9。审计日志记录完整链路（用户/agent/理由/指令/结果），不可篡改存储。
4. **租户边界**：见 AD-10。

## 5. 分期（详见 docs/development-roadmap.md）

- **Stage 0** 工程基础设施：monorepo、CI/CD、测试框架、k8s 部署基线；
- **Stage 1** 单租户端到端 MVP：**openGauss（PG 协议族）蓝本**全链路
  （接入[直连/Connector]→采集→agent→skill→控制台）；
- **Stage 2** 多租户与安全：RLS、RBAC、审批流、凭据与证书体系；
- **Stage 3** 引擎扩展与知识/记忆：MySQL 协议族、达梦、Graphiti 记忆库、合规检查包；
- **Stage 4** 生产化：报表、高可用、部署打包（**计费不做**，2026-08-10 user 定）。

## 6. 修订历史

| 日期 | 变更 | 批准 |
|---|---|---|
| 2026-08-08 | 初版（brainstorming 收敛） | user |
| 2026-08-08 | 新增 AD-11（Agent 核心 = codexgo 抽核）、AD-12（Skill 协议 = MCP）；技术栈联动变更：Agent Runtime Python→Go、Skill gRPC→MCP、新增记忆服务组件 | user（选型问答确认） |
| 2026-08-10 | AD-2 增平台直连模式（本地部署 k8s 为主）、AD-4 改分模式凭据边界（直连凭据平台加密保管）；MVP 蓝本 MySQL→openGauss（PG 协议族），MySQL 族移至 Stage 3；Stage 4 移除计费系统 | user（roadmap 三项调整指示） |
