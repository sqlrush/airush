# 解耦架构：可替换点与契约边界

> 日期：2026-08-09 · 状态：定稿 · 原则来源：用户需求第 6 项——"更换 agent 类型、图库、
> 向量库、记忆库、skill 对架构侵入性不大"。
> 方法：端口与适配器（ports & adapters）。每个可替换点定义一个**平台拥有的契约**
> （接口/协议），实现方作为适配器插入；上层永远只依赖契约。

## 1. 可替换点总表

| # | 可替换点 | 契约（平台拥有） | 当前实现 | 替换动作 | 侵入范围 |
|---|---|---|---|---|---|
| R1 | Agent 核心 | 会话调度器 ↔ agent core 之间的 **AgentCore 接口**（Go interface：StartTurn/ResumeThread/Interrupt/Events） | codexgo 抽核 | 实现新适配器（如接 LangGraph 服务） | agent-runtime 内部，外部 API 不变 |
| R2 | Skill | **MCP 协议** + 注册表 schema | 各 skill 容器 | 换镜像/换语言重写，注册表 endpoint 一改即可 | 零（协议标准化的直接红利） |
| R3 | 记忆库 | **记忆服务 HTTP API**（write/search/forget/timeline，平台自定义 OpenAPI） | Graphiti+Neo4j | 记忆服务内换适配器（Mem0/自建），API 不动 | 记忆服务内部 |
| R4 | 图数据库 | Graphiti 的 GraphDriver 抽象（其原生支持 Neo4j/FalkorDB） | Neo4j | 换 driver 配置 + 数据迁移脚本 | 记忆服务配置层 |
| R5 | 向量库（文档 RAG） | **知识检索 API**（控制面内 repository 接口） | pgvector | 实现 Qdrant repository + 向量重建（embedding 可重算，非关键数据） | 控制面检索模块 |
| R6 | LLM 供应商 | **OpenAI 兼容 API**（LiteLLM 网关北向） | DeepSeek/Qwen/GLM/Claude | 网关配置增删供应商 | 零 |
| R7 | 时序库 | **数据接入层写 API + 查询 repository** | TimescaleDB | 实现 VictoriaMetrics 适配器（写路径已隔离） | 数据接入层内部 |
| R8 | 数据库引擎接入 | **Connector 采集器 SPI**（Go interface：Collect/Probe/Execute，按协议族） | MySQL/PG/达梦驱动 | 新引擎实现 SPI | Connector 内部 |
| R9 | **LLM 网关本体**（spec-1.7，2026-08-15 补） | 北向 **OpenAI 兼容 API**（agent 客户端只认它）+ 平台侧 `libs/llm.Meter`（配额/用量在调用方进程与控制面，网关无状态、无 DB、无缓存） | LiteLLM（digest 钉版）| 换 Bifrost / 自研 Go 网关：改 Helm llm 组件 + `AIRUSH_AGENT_LLM_URL`；配额/用量数据与逻辑零改动 | Helm 一个组件；上层零 |

## 2. 三条契约纪律（CLAUDE.md 规则 8 联动，lint/review 强制）

1. **契约文件唯一来源**：跨服务契约一律进 `proto/`（gRPC/protobuf）或 `api/`
   （OpenAPI YAML）目录，代码由契约生成，禁止手写两份；MCP 工具 schema 由 skill
   自声明、注册表存档为审计副本；
2. **禁止越界依赖**（golangci-lint depguard / import-linter 强制）：
   - agent-runtime 禁止 import 任何存储驱动（neo4j/qdrant driver）——只准走记忆服务
     与知识检索 API；
   - 控制面禁止 import codexgo 包——只准走 agent-runtime 服务 API；
   - skill 禁止 import 平台内部包——只依赖 MCP SDK 与取数 SDK（独立发布的薄包）；
3. **替换演习**：每个可替换点必须有第二实现的 conformance 测试套件
   （contract test，跑在契约上而非实现上）；R3/R5 在 Stage 3 引入正式实现时，
   以 conformance 套件验证（in-memory fake 即第二实现，Stage 1 就建立）。

## 3. 有意不解耦的地方（YAGNI 声明）

- 控制面与 PG：RLS 是多租户安全根基，不为"可换 MySQL"抽象掉 PG 特性；
- Connector 与 Go：单二进制交付是产品需求，不做多语言 Connector SPI；
- k8s：编排层是既定平台约束，不抽象"可脱离 k8s 运行"。

## 4. 修订历史

| 日期 | 变更 |
|---|---|
| 2026-08-09 | 初版 |
| 2026-08-15 | 增 R9 LLM 网关本体（spec-1.7 落地：LiteLLM 无状态纯路由 + 平台侧 Meter，配额/用量不在网关，故网关整体可换） |
