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

1. **codexgo 定向同步（前置专项 P1，2026-08-16 修订）**：簇 A（MCP 2026-07-28）已在 codexgo
   主线完成（spec 49，v0.5.0）；其余对齐工作（线程模型接口、multiagent 失败上抛、core 五块）
   **并入 spec-1.8 的 D0**，在 codexgo 抽核分支 `airush-core` 实施，不再是独立前置
   （盘点依据 `docs/codexgo-diff-inventory-{bcd,core}.md`）；
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
2. **端到端优先**（2026-08-10 修订）：Stage 1 只做 **PG 协议族，以 openGauss 为蓝本**，
   但打通全链路（接入[直连/Connector]→采集→agent→skill→控制台），宁可窄而通，不可宽而断；
   MySQL 协议族移至 Stage 3；
3. **安全前置**：凭据边界、租户边界、审计从 Stage 1 就按终态设计埋点，Stage 2 只是完善而非重构；
4. **存储渐进**（2026-08-15 修订）：MVP 用 PostgreSQL 全家桶（PG+TimescaleDB+pgvector）；
   Neo4j/Graphiti **提前到 Stage 1**——user 定"先把智能体框架全部搭建出来，再做具体的
   数据库功能"，记忆/知识库是智能体框架的一部分，不再等 Stage 3。原"避免早期运维负担"
   的考量让位于框架完整性；运维负担由 Helm 内置形态 + spec-1.18 的备份/恢复 DoD 承接；
5. **部署与商业化取向（2026-08-10 user 定）**：当前以**本地部署 k8s 为主**——存储
   Helm 内置（storage.builtin）为主形态，云托管为后续演进选项；**不做计费系统**，
   LLM 用量统计仅用于成本观测与配额（spec-1.7），不进入计费/账单链路；
6. **接入双模式（AD-2 修订）**：平台直连（同内网免装 Connector，MVP 优先）与
   Connector 反向隧道（跨网场景）并存，采集探针与白名单执行逻辑两模式复用同一代码。

## 1. Stage 总览

```
Stage 0 → Stage 1 → Stage 2 → Stage 3 → Stage 4
基础设施 → 单租户MVP(含框架+记忆) → 多租户安全 → 引擎与深化 → 生产化GA
(~6 周)   (~16-20 周，08-15 重估)  (~8-10 周)   (~8-10 周)  (~8-12 周)
```

**禁止跨 Stage 跳跃**：Stage N 验收 spec 未通过不得开始 Stage N+1 功能点。
例外：跨 Stage 的"基础设施前置"（如 Stage 0 的可观测性框架被所有后续 Stage 使用）。

### 1.1 各阶段交付物（验收标准）

| Stage | 交付物 | 验收标准 |
|---|---|---|
| 0 | 工程基础设施 | CI 全绿；hello-world 服务经 Helm 部署到 kind 集群；测试/lint/构建链路全通 |
| 1 | 单租户端到端 MVP | 真实 openGauss 实例接入（直连 + Connector 双模式）→ 自动巡检出报告 → 诊断对话可用；**agent 记忆跨会话生效 + 知识库问答可用**（2026-08-15 提前）；全程审计留痕 |
| 2 | 多租户可售版本 | 双租户隔离验证（数据/证书/配额互不可见）；动作类操作走审批流；安全扫描通过 |
| 3 | 全引擎 + 智能深化 | 三协议族接入可用；性能基线学习生效；合规检查包出报告（记忆跨会话已提前至 Stage 1） |
| 4 | GA | HA 演练通过；本地部署打包可交付 |

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

## 3. Stage 1：单租户端到端 MVP（18 个功能点，**openGauss（PG 协议族）**）

> 标题原写"MySQL 协议族"，是 2026-08-10 蓝本对调（MySQL→openGauss）时漏改的陈迹，
> 2026-08-14 user 指出并更正。MVP 蓝本是 **openGauss**；MySQL 族在 Stage 3（spec-3.1）。
>
> **2026-08-15 顺序修订（user 定）**："先把智能体框架全部搭建出来，再做具体的数据库功能。"
> 采集组（1.1-1.5）已 shipped，此后按 **框架组 → 记忆/知识库组 → 数据库功能组** 排：
> - 框架组：1.7 LLM 网关 → 1.8 Agent Runtime → 1.9 Skill 框架；
> - 记忆/知识库组（**自 Stage 3 提前**，编号追加 1.18-1.20，原 3.4/3.5/3.6 编号保留不复用）；
> - 数据库功能组：1.10/1.11/1.12 三个 skill、1.13/1.14 前端、1.15 审计、1.16 验收；
> - **1.6 脱敏移出 MVP**（user 定"脱敏不在 MVP 里"），归 Stage 2 安全组排期，编号保留。
>   AD-3 不受损：MVP 内 skill 消费的是采集侧**规范化**产物（字面量已占位），真实客户
>   数据出内网前的二次加固在 Stage 2 补齐。
>
> 工期：Stage 1 原估 12-14 周，记忆/知识库组提前带来 **+4-6 周**（Neo4j+Graphiti 引入、
> 记忆服务、知识库管道），§0.2 的"偏差超 1 个 milestone 须重估"在此触发——本修订即重估。

### 3.1 功能点清单（按当前排定顺序）

| Spec | 名称 | 一句话说明 |
|---|---|---|
| spec-1.1 | 控制面领域模型与 API 骨架 | tenant/user/datasource/agent/skill 注册表 CRUD |
| spec-1.2 | Connector 核心 | 注册、outbound mTLS 长连接、心跳、指令通道（**参照模板级 spec**） |
| spec-1.17 | 直连接入模式（AD-2②，编号追加、逻辑归接入组） | 平台直连数据库：凭据平台侧加密保管（AD-4 直连模式）、直连接入器复用探针/白名单执行代码、接入向导双模式选择 |
| spec-1.3 | openGauss（PG 协议族）采集：指标 | 周期采集性能指标，结构化上报，通道无关（直连/Connector 同一探针） |
| spec-1.4 | openGauss（PG 协议族）采集：慢日志与元数据 | 慢查询、表结构、实例配置采集 |
| spec-1.5 | 数据接入层与时序存储 | 接入网关落库 TimescaleDB，数据模型定版 |
| **spec-1.7** | LLM 网关 | LiteLLM 集成、模型路由、按租户配额与用量统计 |
| **spec-1.8** | Agent Runtime 骨架（codexgo 抽核服务化） | 核心包 vendor、threadstore→PG、租户上下文、会话调度器、无状态 Pod 排水（见 agent-core-design.md） |
| **spec-1.9** | Skill 框架（MCP） | 注册表、MCP streamable HTTP 调用面、幂等与背压、skill 容器 Deployment 形态（见 skill-runtime-design.md） |
| **spec-1.18** | 记忆库 Graphiti+Neo4j 集成（原 spec-3.4，2026-08-15 提前） | Neo4j Helm 内置、Graphiti 记忆服务、写读 API、备份/恢复（见 memory-knowledge-architecture.md） |
| **spec-1.19** | agent 记忆读写策略与生命周期（原 spec-3.5，提前） | 记忆分层/归属、沉淀与遗忘策略、跨会话生效 |
| **spec-1.20** | 知识库服务（图谱 + pgvector RAG）（原 spec-3.6，提前） | 文档摄入管道、embedding 服务、双向量索引、检索面 |
| spec-1.10 | skill：巡检报告 | 基于采集数据生成实例健康巡检报告 |
| spec-1.11 | skill：慢查询分析 | 慢日志聚类、执行计划解读、优化建议 |
| spec-1.12 | skill：健康诊断对话 | 交互式诊断，组合调用采集数据与其他 skill |
| spec-1.13 | 控制台前端：数据库模块与巡检视图 | 数据库模块（列表/拓扑双视图、接入向导、主备/集群关系、Agent 管理域、节点下钻实例详情）、巡检报告展示 |
| spec-1.14 | 控制台前端：通用对话工作台 | 通用对话窗口（诊断/问答/长任务，登录默认落地页）、内容块渲染器注册表、历史会话管理 |
| spec-1.15 | 审计日志基线 | 全链路审计事件模型与查询界面 |
| spec-1.16 | Stage 1 验收 | 真实 **openGauss** 端到端 demo + 性能/成本基线报告（原写 MySQL，2026-08-14 更正） |
| ~~spec-1.6~~ | ~~客户侧脱敏规则引擎~~ | **移出 MVP**（2026-08-15 user 定），归 Stage 2 安全组排期，编号保留不复用 |

### 3.2 Stage 1 验收标准

- [ ] 接入一个真实 openGauss 实例全程 ≤ 30 分钟（含一个 PostgreSQL 实例验证 PG 协议族抽象；
      直连与 Connector 两种接入方式各验证其一）；
- [ ] 自动巡检产出报告，慢查询分析给出可执行建议；
- [ ] 诊断对话能回答"这个实例昨晚为什么慢"级别的问题并引用采集数据；
- [ ] 所有 agent/skill 调用与 LLM 用量有审计与成本记录；
- [ ] 单实例采集端到端延迟与平台资源占用有基线数据；
- [ ] **agent 记忆跨会话生效**（自 Stage 3 验收标准提前，随 1.18/1.19）：新会话能引用
      上一会话沉淀的实例记忆；agent-runtime pod 重建后记忆不丢；
- [ ] **知识库问答可用**（随 1.20）：对已摄入文档的问题能给出带出处的回答。

## 4. Stage 2-4：粗粒度规划（进入前细化）

> 以下清单在对应 Stage 启动前按届时认知细化，允许增删，但增删需更新本文档。

### 4.1 Stage 2：多租户与安全（11 个功能点）

spec-2.1 租户模型与 RLS 全面启用 / spec-2.2 认证与 RBAC / spec-2.3 Connector 证书签发与生命周期 /
spec-2.4 客户侧凭据保管 / spec-2.5 审批工作流与一次性令牌 / spec-2.6 受控执行器与操作白名单 /
spec-2.7 平台侧 secret 管理 / spec-2.8 速率限制与租户配额 / spec-2.9 安全扫描与渗透基线 /
**spec-1.6 客户侧脱敏规则引擎（自 Stage 1 移入，2026-08-15；编号保留）** / spec-2.10 Stage 2 验收。

### 4.2 Stage 3：引擎扩展与深化（7 个功能点）

spec-3.1 MySQL 协议族接入（2026-08-10 与 PG 族对调）/ spec-3.2 达梦接入 / spec-3.3 引擎特有巡检包（TiDB/OceanBase）/
~~spec-3.4 记忆库~~ ~~spec-3.5 记忆策略~~ ~~spec-3.6 知识库~~（**提前至 Stage 1 为 1.18/1.19/1.20**，2026-08-15；
原编号保留不复用）/ spec-3.7 性能基线学习 / spec-3.8 合规检查包 /
spec-3.9 巡检调度中心 / spec-3.10 Stage 3 验收。

### 4.3 Stage 4：生产化（6 个功能点）

spec-4.1 ~~计费与用量~~（**不做**，2026-08-10 user 定；编号保留不复用）/ spec-4.2 报表中心 /
spec-4.3 高可用与容灾 / spec-4.4 大客户独立池 / spec-4.5 部署打包（Helm 离线包 + 本地模型）/
spec-4.6 SLA 监控与状态页 / spec-4.7 GA 验收。

### 4.4 远期候选：数据库管理动作类 skill（**不排期，2026-08-14 user 定**）

> **当前不展开。** user 定调：现阶段全部功能围绕「基于 k8s 容器的智能体 + 记忆 /
> 知识库等平台能力」展开；主备 / 集群 / 容灾等**管理动作**类 skill 整体归远期候选，
> 不进任何 Stage 排期，不起草 spec。本节只做登记，避免同一讨论反复从零开始。

已识别的候选面（讨论产物，非承诺）：

| 组 | 内容 |
|---|---|
| 基座 | 拓扑关系边表（`datasource_relations`）、操作工单模型与 op_type 目录、操作 dry-run 与影响评估 |
| 主备 | 拓扑发现与漂移、复制健康诊断、switchover、failover、备库重建 |
| 集群 | 节点健康、容量均衡分析、扩容 / 下线 / 滚动重启 |
| 容灾 | 配对状态、就绪度实测（RPO/RTO）、演练、灾备切换、回切 |

**两条必须随之带走的约束**（真做时的前置，不因归档而失效）：

1. **Connector 执行面边界**：当前受控执行器是「命令类型白名单 + 载荷零 SQL」的只读探针
   （spec-1.2/1.4）。上述动作需要 `gs_ctl`/`cm_ctl` 一类**系统命令**，属 AD-9 操作边界的
   实质扩张（改变 Connector 被攻破后的爆炸半径）。启动该组前必须先定 AD 级决策，
   不得以"加个 skill"的形态混入；
2. **拓扑表达缺口**：现模型（`datasource_groups` + `group_role`）是扁平分组，
   表达不了组与组之间的关系（容灾配对、级联复制、双活）。spec-1.5 §1「不包含」已登记。

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
| spec-0.1 | **shipped**（T1-T5 全过，Mac 宿主机验证） | 2026-08-08 起草；08-10 实施完成 |
| spec-0.2 | **shipped**（T1-T7 全过，含注入验证） | 2026-08-09 起草；08-10 实施完成 |
| spec-0.3 | **shipped**（T1/T2/T4-T7 过；T3 分支保护受 GitHub Free 限制，待 user 决策 Pro/public/约定式） | 2026-08-09 起草；08-10 实施完成 |
| spec-0.4 | **shipped**（T1-T7 过；阻断开关 COVER_ENFORCE 留待 spec-1.1 打开） | 2026-08-09 起草；08-10 实施完成 |
| spec-0.5 | **shipped**（T1-T7 过；ci/integration 入 required checks） | 2026-08-09 起草；08-10 实施完成 |
| spec-0.6 | **shipped**（T1-T8 过；表结构经 user 评审；RLS 模板经集成实证含连接池 GUC 空串修正） | 2026-08-09 起草；08-10 实施完成 |
| spec-0.7 | **shipped**（T1-T8 过；自研薄层替代 caarlos0/env，见 spec 修订） | 2026-08-09 起草；08-10 实施完成 |
| spec-0.8 | **shipped**（T1-T8 过；15 码 JSON SSOT 双语言生成；apierror 覆盖率 94.6%） | 2026-08-09 起草；08-10 实施完成 |
| spec-0.9 | **shipped**（T1-T8 过；三信号冒烟脚本全绿含双出口脱敏实证；libs/obs 覆盖率 82.7%） | 2026-08-09 起草；08-10 实施完成 |
| spec-0.10 | **shipped**（dev-up 一键全栈 + DEV VERIFY ALL PASS；Go 镜像 <30MB 达标） | 2026-08-09 起草；08-10 实施完成 |
| spec-0.11 | **shipped**（T1-T7 过；v0.0.1-rc.1 全链路实弹演练成功） | 2026-08-09 起草；08-10 实施完成 |
| spec-0.12 | **shipped**（验收签署，自动项 7/7） | 2026-08-09 起草；08-10 验收执行 |
| spec-1.1 | **shipped**（T1-T10 全过；覆盖率合并口径 81.9%；dev-verify 端到端）| 2026-08-10 起草、approve、实施完成 |
| spec-1.2 | **frozen · 实施完成**（T1-T12 全过；dev-verify connector e2e online；覆盖率合并口径达标）| 2026-08-11 起草、approve、实施 |
| spec-1.17 | **frozen · 实施完成**（T1-T10 全过；directconn 真 PG 集成；覆盖率达标）| 2026-08-11 起草、approve、实施 |
| spec-1.3 | **frozen · 实施完成**（T1-T11 全过；一套探针两通道：Direct 本地探针+Connector DataUpload→gateway Sink；真 PG 集成；覆盖率合并口径达标 libs-metrics 94.6%/connector 84.8%/gateway 81.1%/console 82.3%；dev-verify Direct 采集心跳可见）| 2026-08-11 起草、approve；08-12 实施完成 |
| spec-1.4 | **frozen · 实施完成**（T1-T13 全过；真 openGauss 5.0.3 校准出 dbe_perf 两处列名错 + 表结构排序错 + spec-1.3 遗留的复制延迟方言错；字面量金丝雀在真机实证；覆盖率 connector 87.3%/console 82.2%/gateway 81.5%/libs-metrics 84.7%；dev-verify ALL PASS 三类快照心跳可见）| 2026-08-12 起草并 approve；08-13 实施完成 |
| spec-1.5 | **shipped**（T1-T22 全过；AD-10 等效隔离四项门槛 T7-T10 全绿——其中"越权写被拒"初验时没拦住，补 `check_option` 才堵上；R1 基准经视图写入退化 **5.9%**（门槛 30%）故 §8 Q2 选项 A 落地；表数收敛承诺兑现：采集侧固定 3 张表，加采集能力/加引擎只加编译期目录常量；实施中修掉采集器租户上下文漏传（内存 Sink 掩盖了它）与 kind 冷启动两处工程缺陷；覆盖率 console 83.4%/libs-metrics 86.3%/connector 87.3%/gateway 81.0%；dev-verify ALL PASS 含 Direct 与 Connector 两通道落库；code review 后 Top N 排名端点显式 501 移交 spec-1.11、上报链补数据源归属校验）| 2026-08-14 起草并 approve；08-15 实施完成 + review 收口 |
| spec-1.7 | **shipped**（T1-T21 全过，集成用真 LiteLLM 容器；默认模型 **Kimi K3**（`api.kimi.com/coding/v1`）经集群内网关 + Secret 注入验通 chat/流式 usage/Responses/function_call；LiteLLM 无状态纯路由、配额/用量平台侧（`libs/llm.Meter` + console 内部 API）；实测修正：LiteLLM 空载 ~1.05 GiB、Responses 桥接依赖供应商原生前缀、/metrics 需 key；覆盖率 console 83.1%/libs-llm 88.9%/libs-tenancy 100%；dev-verify ALL PASS 含 T19-T21）| 2026-08-15 起草、approve、实施完成 |
| spec-1.8 | **DRAFT · 待 approve**（范围按 2026-08-16 user 定：airush runtime 骨架 + codexgo 侧全部对齐工作（D0，抽核分支 `airush-core`：ThreadStore 对齐 0.147 / id v7 / steer 准入 / 上下文窗口与压缩 / 集中审批阶段 / 客户端健壮性 / 协议新增 / multiagent 失败上抛 / 删 goals·agent_jobs）+ threadstore-PG（0006 四表）+ 租户贯穿与 LLM 接线 + 调度器与 HTTP/SSE 入口 + 无状态排水恢复 + AD-9 审批阶段占位；估 6 周）| 2026-08-16 起草 |
| 其余 Stage 1 specs | 按修订后顺序起草（1.9 → 1.18-1.20 → …，严格事前 approve） | — |
