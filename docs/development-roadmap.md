# AIRush 开发路线图 v1.0

> 状态：**user approved（2026-08-09，"开始基于 roadmap 写每个 spec"指示视为认可）**
> 配套文档：`docs/2026-08-08-airush-platform-design.md`（总体设计 SSOT）、`CLAUDE.md`（研发纪律）
> 本文档是开发节奏与 spec 拆分的权威来源，进度表随每个 spec 的 frozen/shipped 状态持续更新。

## 0. 概述

### 0.1 当前状态（2026-08-09）

- 总体设计已 user approve，含 AD-11（Agent 核心 = codexgo 抽核）、AD-12（skill 协议 = MCP）；
- 专项设计已完成：agent 抽核、存储选型、skill 运行时、k8s 伸缩、解耦架构、开发规范、
  UI 纲要（见 docs/ 各文档）；codexgo 上游同步采用策略 C（见 codexgo-sync-assessment.md）；
- 仓库已建立（github.com/sqlrush/airush，私有）；
- 尚未编写任何产品代码——**Stage 0 第一个 spec approve 前不开始编码**。

### 0.4 编码前置门槛（user 定，2026-08-08）

1. **codexgo 定向同步（前置专项 P1）**：P0 功能簇（MCP 2026-07-28、线程模型、
   multiagent v2、token 预算）在 codexgo 仓完成，是 spec-1.8/1.9 的硬前置；
   与 airush Stage 0 可并行；
2. **UI 设计（前置专项 P2）**：高保真 mockup 评审定稿 + UI 设计规范沉淀，
   是 spec-1.13/1.14 的硬前置（见 ui-design-brief.md §5）；与 Stage 0/1 后端可并行。

### 0.2 工程规模评估（实事求是）

平台含 6 个可独立部署组件（Connector、控制面 API、接入网关、Agent Runtime、Skill 服务、前端）、
5 类存储、3 个数据库协议族接入。这不是一个"两周出 demo"的项目：
以 1-2 名全职工程师 + AI 协作的投入估算，**到单租户 MVP（Stage 1 完成）约 4-5 个月，
到多租户可售（Stage 2 完成）约 7-8 个月，到 GA（Stage 4 完成）约 12-14 个月**。
估算偏差超过 1 个 milestone 时必须回到本文档重估节奏。

### 0.3 核心策略

1. **纪律优先**：每个功能点严格走 5 阶段流程（见第 5 节），spec 未 approve 不编码；
2. **端到端优先**：Stage 1 只做 MySQL 协议族，但打通全链路（Connector→采集→agent→skill→控制台），
   宁可窄而通，不可宽而断；
3. **安全前置**：凭据边界、租户边界、审计从 Stage 1 就按终态设计埋点，Stage 2 只是完善而非重构；
4. **存储渐进**：MVP 用 PostgreSQL 全家桶（PG+TimescaleDB+pgvector），Neo4j/Graphiti 到 Stage 3
   有真实记忆需求时引入，避免早期运维负担。

## 1. Stage 总览

```
Stage 0 → Stage 1 → Stage 2 → Stage 3 → Stage 4
基础设施 → 单租户MVP → 多租户安全 → 引擎与记忆 → 生产化GA
(~6 周)   (~12-14 周)  (~8-10 周)   (~10-12 周)  (~8-12 周)
```

**禁止跨 Stage 跳跃**：Stage N 验收 spec 未通过不得开始 Stage N+1 功能点。
例外：跨 Stage 的"基础设施前置"（如 Stage 0 的可观测性框架被所有后续 Stage 使用）。

### 1.1 各阶段交付物（验收标准）

| Stage | 交付物 | 验收标准 |
|---|---|---|
| 0 | 工程基础设施 | CI 全绿；hello-world 服务经 Helm 部署到 kind 集群；测试/lint/构建链路全通 |
| 1 | 单租户端到端 MVP | 真实 MySQL 实例接入 → 自动巡检出报告 → 诊断对话可用；全程审计留痕 |
| 2 | 多租户可售版本 | 双租户隔离验证（数据/证书/配额互不可见）；动作类操作走审批流；安全扫描通过 |
| 3 | 全引擎 + 智能深化 | 三协议族接入可用；agent 记忆跨会话生效；合规检查包出报告 |
| 4 | GA | 计费闭环；HA 演练通过；私有化打包可交付 |

## 2. Stage 0：工程基础设施（12 个功能点）

### 2.1 功能点清单

| Spec | 名称 | 一句话说明 |
|---|---|---|
| spec-0.1 | monorepo 脚手架与构建体系 | 目录结构、Go workspace、Python uv、pnpm、根 Makefile |
| spec-0.2 | 代码风格与 lint | golangci-lint / ruff+mypy / eslint+prettier / editorconfig |
| spec-0.3 | CI/CD pipeline | GitHub Actions：lint→test→build→镜像，全绿门槛 |
| spec-0.4 | 单元测试框架与覆盖率门槛 | 三语言测试骨架 + 80% 覆盖率闸门 |
| spec-0.5 | 集成测试框架 | testcontainers + docker compose 测试环境 |
| spec-0.6 | 控制面数据库 schema 与迁移框架 | PG 迁移工具链 + tenant_id/RLS 表设计约定 |
| spec-0.7 | 配置框架 | 12-factor 配置加载、secret 注入约定、启动时校验 |
| spec-0.8 | 错误码与 API 错误规范 | 统一错误码空间、API 错误响应格式、错误分级 |
| spec-0.9 | 可观测性基线 | OpenTelemetry：结构化日志/metrics/tracing 三件套 |
| spec-0.10 | 镜像构建与 Helm chart 骨架 | 多阶段 Dockerfile 规范、Helm 结构、kind 本地环境 |
| spec-0.11 | 版本号与 release 节奏 | 语义化版本、tag 规范、CHANGELOG 维护 |
| spec-0.12 | Stage 0 验收 | hello-world 全链路：CI 全绿 + Helm 部署 kind + 可观测性可见 |

### 2.2 Stage 0 验收标准

- [ ] 三语言（Go/Python/TS）各有一个可构建、可测试、可 lint 的最小包；
- [ ] PR 必须 CI 全绿才能合并 main（分支保护生效）；
- [ ] `make dev-up` 一键拉起 kind + 部署 hello-world 服务；
- [ ] hello-world 服务的日志/指标/trace 在本地可观测栈中可见；
- [ ] 控制面 PG 迁移框架跑通第一个 migration。

## 3. Stage 1：单租户端到端 MVP（16 个功能点，MySQL 协议族）

### 3.1 功能点清单

| Spec | 名称 | 一句话说明 |
|---|---|---|
| spec-1.1 | 控制面领域模型与 API 骨架 | tenant/user/datasource/agent/skill 注册表 CRUD |
| spec-1.2 | Connector 核心 | 注册、outbound mTLS 长连接、心跳、指令通道（**参照模板级 spec**） |
| spec-1.3 | MySQL 协议族采集：指标 | 周期采集性能指标，结构化上报 |
| spec-1.4 | MySQL 协议族采集：慢日志与元数据 | 慢查询、表结构、实例配置采集 |
| spec-1.5 | 数据接入层与时序存储 | 接入网关落库 TimescaleDB，数据模型定版 |
| spec-1.6 | 客户侧脱敏规则引擎 | Connector 内置脱敏，规则可配置 |
| spec-1.7 | LLM 网关 | LiteLLM 集成、模型路由、按租户配额与用量统计 |
| spec-1.8 | Agent Runtime 骨架（codexgo 抽核服务化） | 核心包 vendor、threadstore→PG、租户上下文、会话调度器（见 agent-core-design.md） |
| spec-1.9 | Skill 框架（MCP） | 注册表、MCP streamable HTTP 调用面、幂等与背压（见 skill-runtime-design.md） |
| spec-1.10 | skill：巡检报告 | 基于采集数据生成实例健康巡检报告 |
| spec-1.11 | skill：慢查询分析 | 慢日志聚类、执行计划解读、优化建议 |
| spec-1.12 | skill：健康诊断对话 | 交互式诊断，组合调用采集数据与其他 skill |
| spec-1.13 | 控制台前端：数据库模块与巡检视图 | 数据库模块（列表/拓扑双视图、接入向导、主备/集群关系、Agent 管理域、节点下钻实例详情）、巡检报告展示 |
| spec-1.14 | 控制台前端：通用对话工作台 | 通用对话窗口（诊断/问答/长任务，登录默认落地页）、内容块渲染器注册表、历史会话管理 |
| spec-1.15 | 审计日志基线 | 全链路审计事件模型与查询界面 |
| spec-1.16 | Stage 1 验收 | 真实 MySQL 端到端 demo + 性能/成本基线报告 |

### 3.2 Stage 1 验收标准

- [ ] 接入一个真实 MySQL 实例（含一个 TiDB 实例验证协议族抽象）全程 ≤ 30 分钟；
- [ ] 自动巡检产出报告，慢查询分析给出可执行建议；
- [ ] 诊断对话能回答"这个实例昨晚为什么慢"级别的问题并引用采集数据；
- [ ] 所有 agent/skill 调用与 LLM 用量有审计与成本记录；
- [ ] 单实例采集端到端延迟与平台资源占用有基线数据。

## 4. Stage 2-4：粗粒度规划（进入前细化）

> 以下清单在对应 Stage 启动前按届时认知细化，允许增删，但增删需更新本文档。

### 4.1 Stage 2：多租户与安全（10 个功能点）

spec-2.1 租户模型与 RLS 全面启用 / spec-2.2 认证与 RBAC / spec-2.3 Connector 证书签发与生命周期 /
spec-2.4 客户侧凭据保管 / spec-2.5 审批工作流与一次性令牌 / spec-2.6 受控执行器与操作白名单 /
spec-2.7 平台侧 secret 管理 / spec-2.8 速率限制与租户配额 / spec-2.9 安全扫描与渗透基线 /
spec-2.10 Stage 2 验收。

### 4.2 Stage 3：引擎扩展与知识/记忆（10 个功能点）

spec-3.1 PG 协议族接入 / spec-3.2 达梦接入 / spec-3.3 引擎特有巡检包（TiDB/OceanBase）/
spec-3.4 记忆库 Graphiti+Neo4j 集成 / spec-3.5 agent 记忆读写策略与生命周期 /
spec-3.6 知识库服务（图谱 + pgvector RAG）/ spec-3.7 性能基线学习 / spec-3.8 合规检查包 /
spec-3.9 巡检调度中心 / spec-3.10 Stage 3 验收。

### 4.3 Stage 4：生产化与商业化（7 个功能点）

spec-4.1 计费与用量 / spec-4.2 报表中心 / spec-4.3 高可用与容灾 / spec-4.4 大客户独立池 /
spec-4.5 私有化打包（Helm 离线包 + 本地模型）/ spec-4.6 SLA 监控与状态页 / spec-4.7 GA 验收。

## 5. 单功能点研发 5 阶段流程（强制）

```
SPEC（编码前讨论） → TDD 编码 → 集成测试 → Code Review → Release & CI/CD
```

1. **SPEC**：按 CLAUDE.md 规则 2 起草 `specs/spec-N.M-<slug>.md`，DRAFT 状态 push 供 user 评审；
   **user approve 是进入编码的硬门槛**；
2. **TDD 编码**：先写测试再写实现，禁止"功能写完测试后补"；
3. **集成测试**：核心路径集成用例通过，覆盖率达标；
4. **Code Review**：按 review checklist 自查 + review agent 过一遍，安全相关改动加 security review；
5. **Release**：CI 全绿合并 main，更新 spec 状态、CHANGELOG、本文档进度。

## 6. SPEC 文档模板

每个 spec 必含以下 10 节（详细度量化门槛见 CLAUDE.md 规则 2）：

1. **Header/元数据**：背景（Stage 内位置 + 前置 spec 关系）、关联文档、DRAFT/approve 状态；
2. **§1 范围**：包含（Deliverable 编号 + 文件清单 + LOC 估算）/ 不包含（+ 理由）/ 例外说明；
3. **§2 接口设计**：API 签名、数据结构、schema、配置项表；
4. **§3 行为契约**：边界条件语义、错误传播路径、兼容性保证；
5. **§4 测试用例**：单元/集成用例编号清单，每条一句话目的；
6. **§5 与现有代码的 contract**：动哪些模块、不动哪些、接口兼容性表态；
7. **§6 风险**：≥5 条，含概率与缓解措施，禁泛泛之词；
8. **§7 DoD**：≥10 条可勾选清单，覆盖代码/测试/文档/部署全链；
9. **§8 Q&A**：≥4 个决策点，每个 ≥2 选项 + ★推荐 + 理由；
10. **§9 实施计划 + §10 后续 spec 关联**：步骤估时表 + 下游 spec 影响。

## 7. 里程碑与节奏

- 每个 Stage 完成 → 写 `docs/stage-N-retrospective.md` 回顾文档；
- 每月 1 号 → 更新本文档进度表；
- 落后 1 个 milestone → 回到 0.2 节重估；
- 首个可对外演示切点：Stage 1 验收 demo；可售切点：Stage 2 验收。

## 8. 进度表

| Spec | 状态 | 日期 |
|---|---|---|
| spec-0.1 | DRAFT（预批可开工） | 2026-08-08，08-09 修订对齐 AD-11 |
| spec-0.2 | DRAFT（预批可开工） | 2026-08-09 |
| spec-0.3 | DRAFT（预批可开工） | 2026-08-09 |
| spec-0.4 | DRAFT（预批可开工） | 2026-08-09 |
| spec-0.5 | DRAFT（预批可开工） | 2026-08-09 |
| spec-0.6 | DRAFT（预批可开工） | 2026-08-09 |
| spec-0.7 | DRAFT（预批可开工） | 2026-08-09 |
| spec-0.8 | DRAFT（预批可开工） | 2026-08-09 |
| spec-0.9 | DRAFT（预批可开工） | 2026-08-09 |
| spec-0.10 | DRAFT（预批可开工） | 2026-08-09 |
| （其余待起草） | — | — |
