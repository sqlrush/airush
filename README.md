# AIRush — 数据库管理智能体平台

面向企业客户的 SaaS 多租户数据库管理智能体平台：智能运维管理 + 巡检与合规。
支持 MySQL 协议族、PostgreSQL 协议族与达梦，客户侧 Connector 反向隧道接入。

**当前阶段**：设计与规划（Stage 0 尚未开始编码）。

## 文档导航

| 文档 | 内容 |
|---|---|
| [总体设计](docs/2026-08-08-airush-platform-design.md) | 架构、核心决策 AD-1..AD-12、技术栈、安全模型 |
| [开发路线图](docs/development-roadmap.md) | Stage 划分、spec 清单、5 阶段研发流程、编码前置门槛、进度表 |
| [研发纪律](CLAUDE.md) / [开发规范](docs/development-standards.md) | 流程规则与三语言代码规范 |
| [Agent 核心设计](docs/agent-core-design.md) | codexgo 抽核服务化方案（AD-11 落地） |
| [codexgo 同步评估](docs/codexgo-sync-assessment.md) | codex 0.136→0.147 功能盘点与同步策略 C |
| [存储选型](docs/storage-selection.md) | 图/向量/记忆/时序库选型论证与替换路径 |
| [记忆与知识架构详解](docs/memory-knowledge-architecture.md) | Graphiti/Neo4j 分工、检索机制、记忆分层与连续性、文档管道、embedding 部署 |
| [Skill 运行时](docs/skill-runtime-design.md) | 跨容器 MCP 调用、多 agent 复用、Job 型 skill |
| [k8s 伸缩设计](docs/k8s-scaling-design.md) | 各组件伸缩信号矩阵、KEDA/HPA、优雅排水 |
| [解耦架构](docs/decoupling-architecture.md) | 8 个可替换点的契约边界与替换成本 |
| [UI 设计纲要](docs/ui-design-brief.md) | 参考产品、信息架构、设计语言、mockup 交付路径 |
| [Specs 索引](specs/README.md) | 各功能点 spec 状态 |

代码脚手架随 spec-0.1 approve 后落地（`make build && make test` 快速开始届时补充）。
