# 存储技术选型：图数据库 / 向量数据库 / 记忆库 / 时序库

> 日期：2026-08-09 · 状态：定稿（深化 AD-5/6/7 的详细论证）
> 原则：MVP 期最小化运维面（PG 全家桶优先），终态引入专用存储；每类存储都有明确替换路径
> （见 `docs/decoupling-architecture.md`）。

## 1. 记忆库（agent 记忆的读写层）

| 候选 | 优势 | 劣势 | 结论 |
|---|---|---|---|
| **Graphiti**（开源，Zep 底层） | 时序知识图谱：事实带 valid_at/invalid_at，天然回答"何时知道、后来怎么变"；混合检索（语义+BM25+图遍历）；group_id 原生多租户命名空间；支持 Neo4j/FalkorDB 后端 | Python 栈（需独立服务包装）；社区较新 | **★选定** |
| Mem0 | API 简单、生态热；向量为主图为辅 | 时序因果弱（运维场景核心是"故障→处置→效果"时间线）；图记忆能力浅 | 备选，接口层预留切换 |
| Zep Cloud | 托管省事 | SaaS 依赖外部服务，客户数据出境不可接受；私有化版本即 Graphiti | 否决 |
| 自建 pgvector 记忆表 | 运维最简 | 只有相似检索，无因果/时序结构；后期重写成本高 | 仅作 Stage 1 过渡（记忆功能 Stage 3 才上线，实际可跳过过渡） |

**多租户**：Graphiti `group_id = tenant:{tenant_id}`；平台公共运维知识用 `group_id = platform`。

### 1.1 记忆分层与归属（2026-08-09 定，详化于 spec-3.5）

1. **双层记忆结构**：
   - **常驻指令层**（对应 CC 的 CLAUDE.md 职能）：agent/租户级 markdown 指令文档，存控制面
     PG（agent 配置实体的一部分），控制台可编辑、带版本，每 turn 确定性注入——不落本地
     文件（AD-1 无状态约束），不进图谱；
   - **检索层**：经验/情节记忆走 Graphiti → Neo4j，按需混合检索。
   - 不做记忆双写 md（Episode 节点原文即 SSOT）；"导出为 md"仅作审计/迁移视图。
2. **记忆以实体（实例）为中心归租户所有，不设 agent 私有图命名空间**：同租户各 agent
   共享该租户记忆（实例历史是客观事实，按 agent 切分会撕碎时间线）；agent 个性化偏好
   放常驻指令层。检索时并行查 `tenant:{id}` + `platform` 两命名空间，结果带来源标签合并。
3. **沉淀管道（合规红线）**：租户记忆升华为 platform 通用知识必须经
   脱敏 → 泛化（去客户特征）→ 审核 三步人工/半自动管道，禁止自动流动。

## 2. 图数据库（记忆库与知识图谱的存储后端）

| 候选 | 优势 | 劣势 | 结论 |
|---|---|---|---|
| **Neo4j**（Community 起步） | Graphiti 一级支持；自带向量索引（图+向量一套，AD-6 合并前提）；生态与文档最成熟；k8s Helm 官方支持 | Community 版无多库/集群（用 group_id 逻辑隔离顶到中等规模）；Enterprise 授权费是后期成本 | **★选定** |
| FalkorDB | 轻量（Redis 协议）、Graphiti 支持、性能好 | 生态薄；图算法与工具链弱 | 规模化降本备选（接口不变可换） |
| NebulaGraph | 分布式横向扩展强 | Graphiti 不支持；自己写记忆层 = 放弃选型 1 | 否决（除非未来图规模爆炸） |
| Apache AGE（PG 插件） | 与控制面同栈零新增运维 | Graphiti 不支持；Cypher 兼容性残缺；向量还得另配 | 否决 |

## 3. 向量数据库

| 候选 | 用途匹配 | 结论 |
|---|---|---|
| **Neo4j 向量索引** | 记忆/知识图谱内的实体与事实 embedding（Graphiti 原生使用） | **★图谱侧选定** |
| **pgvector** | 纯文档 RAG（runbook、引擎手册、产品文档）；与控制面同栈，RLS 天然多租户 | **★文档侧选定** |
| Qdrant | 独立向量库，过滤与量化能力强 | 文档 RAG 超过 pgvector 舒适区（千万级向量/高 QPS）时的替换目标 |
| Milvus | 十亿级规模 | 运维重（etcd+minio+pulsar），远超本项目需求，否决 |

**分工**：不设"一个统一向量库"——图谱内 embedding 跟图走（Neo4j），文档 embedding 跟
控制面走（pgvector）。两处各自靠近数据，避免跨库一致性问题。

## 4. 时序库（采集指标）

| 候选 | 结论 |
|---|---|
| **TimescaleDB**（PG 插件） | **★选定**：与控制面同栈，SQL 直查，压缩与降采样够用；hypertable 按 tenant_id+instance 分区 |
| VictoriaMetrics | 单机性能与压缩比更强；接入实例 >5000 或写入瓶颈时的替换目标（数据接入层已隔离写路径） |
| Prometheus | 拉模型不适配 Connector 推送架构；仅用于平台自身监控（spec-0.9），不存客户指标 | 

## 5. 存储总览与引入节奏

| 存储 | 角色 | 引入 | 多租户隔离 |
|---|---|---|---|
| PostgreSQL | 控制面 + 会话（threadstore 后端） | Stage 0 | RLS |
| TimescaleDB | 采集指标 | Stage 1 | hypertable 分区键 + RLS |
| pgvector | 文档 RAG | Stage 1 | RLS |
| Redis | 上下文缓存 / 队列 | Stage 1 | key 前缀 `t:{tenant_id}:` + ACL |
| Neo4j + Graphiti | 记忆 + 运维知识图谱 | Stage 3 | Graphiti group_id |

## 6. 修订历史

| 日期 | 变更 |
|---|---|
| 2026-08-09 | 初版：AD-5/6/7 深化论证 + 替换路径 |
