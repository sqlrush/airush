# spec-0.9 可观测性基线

> **approved & shipped** — Stage 0 验收签署 2026-08-10（分级预批流程，spec-0.12）

## Header / 元数据

- **位置**：Stage 0 第 9 个功能点；前置 spec-0.5（compose 轨道）、0.7（配置）、0.8（错误码 label）；
  CLAUDE.md 规则 8"新增服务必须接入三件套后才算完成"的基建面；Stage 0 验收
  （spec-0.12）要求 hello-world 三信号可见；
- **配套规则**：development-standards §1.5（结构化 JSON 日志必带 `tenant_id/trace_id/component`，
  无租户上下文标 `tenant_id: "-"`；禁打印凭据/客户数据原文）；
- **依赖审批**（规则 8）：Go 侧 OTel SDK（`go.opentelemetry.io/otel` 系列）；Python 侧
  `structlog` + OTel SDK。**approve 本 spec 即完成审批**，理由见 §8 Q1/Q2/Q4；
- **决策日期**：2026-08-09；
- **实施修订（2026-08-10）**：① 日志增 **OTLP 双出口**（stdout JSON + otelslog→Loki），
  否则本地栈三信号缺日志一环；② redaction 从单 handler 钩子上移为 **record 级包装器**
  ——obs-smoke 实证单 handler 方案下 Loki 分支泄漏；③ D5 演示端点挂 gateway
  `--serve`（兼 spec-0.12 hello-world 载体）；④ Python 侧 OTel 导出延后至 spec-1.9
  （skill 尚无服务进程），structlog 日志层 + 同步 redaction 清单先行；
  ⑤ resource 用 NewSchemaless 合并（Default 的 semconv schema 版本冲突会静默
  变成 unknown_service）。

## §1 范围

### §1.1 包含（Deliverables）

| # | Deliverable | 文件清单 | 估算 | 说明 |
|---|---|---|---|---|
| D1 | Go 观测库 | `libs/obs/`：slog JSON logger（必带字段自动注入）、OTel tracer/meter 初始化、HTTP server/client 中间件（trace 传播 + 请求指标）、日志 redaction 兜底处理器 | ~6 文件 ~350 LOC | 四组件共享 |
| D2 | Python 观测模块 | `skills/airush_skills/obs.py`：structlog JSON + OTel 初始化 + 同名必带字段 | ~2 文件 ~120 LOC | 与 Go 侧字段 schema 一致 |
| D3 | 命名与标签约定 | 本 spec §2 定版：metric 命名、label 白名单（禁高基数）、trace 传播、采样配置 | 文档内嵌 | 全部后续 spec 的强约定 |
| D4 | 本地观测栈 | `deploy/compose/obs.yml`（grafana/otel-lgtm 单容器）+ `make obs-up/down` + 组件默认 OTLP 端点指向它 | 1 文件 ~40 行 | §8 Q3 |
| D5 | 占位服务接入演示 | gateway 占位服务加 `/healthz` 与 `/demo` 端点：一次请求产生 log+trace+metric 三信号且 trace_id 贯通，README 附查询指引 | ~2 文件 ~80 LOC | spec-0.12 验收的预演 |

### §1.2 不包含

| 不做的事 | 理由 |
|---|---|
| 告警规则与 SLO | 无生产流量无 SLO 基线；Stage 1 验收（spec-1.16）留基线后另立 |
| 生产观测栈选型与部署 | 云托管（阿里云 SLS/Prometheus 或自建）是 Stage 1 末部署议题，本 spec 的 OTLP 出口对两者中立 |
| 正式 Grafana dashboards | 0.12 验收只要求"可查到"；dashboard 随各业务 spec 的真实指标沉淀 |
| 前端 RUM/错误上报 | 前端未成型；Stage 1 前端 spec 按需引入 |
| 审计日志 | **观测 ≠ 审计**：审计是业务事件流（不可篡改、面向合规，spec-1.15），走独立存储；本 spec 明确此边界防混用 |
| k8s 日志采集（DaemonSet/sidecar） | 属部署形态（spec-0.10 后随 Helm 演进）；OTLP 直推模式下暂无需求 |

### §1.3 例外说明

无偏离。libs/obs 与 libs/config 同理满足共享库条件（4 消费方）。

## §2 接口设计

### §2.1 结构化日志（定版）

- 格式：单行 JSON；必带字段 `ts / level / component / tenant_id / trace_id / msg`
  （标准 §1.5，缺租户上下文时 `tenant_id: "-"`）；
- Go 用标准库 slog（§8 Q1），logger 由 libs/obs 构造，业务代码禁自建 logger；
- redaction 兜底：对值匹配已知 secret 模式（`password=`、`Bearer `、AKIA 前缀等保守清单）
  自动打码——spec-0.7 类型防线之外的第二道防线，误伤率优先于查全率。

### §2.2 metrics（定版）

- 命名：`airush_<component>_<name>_<unit>`（如 `airush_gateway_http_requests_total`）；
- **label 白名单**：`component / method / route / code(错误码) / level / skill / model`；
  **禁入 label**：tenant_id、session_id、instance_id 等无界高基数值（租户级用量走
  控制面业务表统计，不走 metrics）；新 label 须修订本 spec；
- 出口：OTLP 推送（§8 Q4），KEDA/HPA 所需指标经 collector 落 Prometheus 兼容存储供 PromQL 查询。

### §2.3 tracing（定版）

- W3C traceparent 传播；HTTP/gRPC/MCP 调用链贯通（中间件负责）；
- span 命名 `<component>.<operation>`；错误 span 记 `code` 属性（spec-0.8）；
- 采样：`AIRUSH_COMMON_TRACE_SAMPLE_RATIO` 配置项，dev 默认 1.0，生产默认 0.1 +
  错误 span 强制保留（§8 Q5）；
- trace_id 即日志字段与 API 错误响应 trace_id（0.8 §2.2），三处同源。

## §3 行为契约

- 观测初始化失败**不阻断服务启动**（降级为 stdout 日志 + no-op tracer，打告警日志）——
  可观测性是辅助面，不得成为可用性单点；
- 中间件对业务 handler 透明（不吞错、不改响应）；
- 日志写入路径无阻塞 IO 放大（异步 buffer，压测于 Stage 1 基线覆盖）；
- 必带字段由框架注入，业务代码手写这四个字段视为缺陷。

## §4 测试用例

| # | 用例 | 目的 |
|---|---|---|
| T1 | /demo 一次请求 → 日志含 trace_id 且与 span、响应 trace_id 三方一致 | 贯通性 |
| T2 | 日志缺租户上下文 → tenant_id="-" | 标准 §1.5 |
| T3 | 注入含 password=xxx 的日志值 → 输出打码 | redaction 兜底 |
| T4 | metric 含白名单外 label → libs/obs 构造期 panic（fail-fast） | 高基数防线 |
| T5 | OTLP 端点不可达 → 服务正常启动 + 告警日志 | 降级契约 |
| T6 | 采样 0 时错误 span 仍导出 | 错误强采 |
| T7 | Python 侧日志字段 schema 与 Go 一致（golden 对比） | 双语言一致 |
| T8 | obs-up 后 /demo 三信号在 Grafana 三个数据源均可查（手工步骤脚本化断言） | 端到端 |

## §5 与现有代码的 contract

- 新增：libs/obs、Python obs、compose obs.yml、演示端点；
- 修改：Makefile、gateway 占位 main（接 obs 初始化）、spec-0.7 配置项清单（新增 OTLP
  端点与采样率，.env.example 同步）；
- 不动：CI 结构、错误库（仅消费其 code）；
- 对后续 spec 的接口：§2.1-2.3 三约定 + "新服务必接三件套"的验收口径（规则 8 引用本 spec）。

## §6 风险

| 风险 | 概率 | 缓解 |
|---|---|---|
| tenant_id 类高基数 label 被后续 spec 引入压垮存储 | 中 | T4 构造期 fail-fast + label 白名单修订门槛（必须过本 spec 修订） |
| OTel Go SDK API 尚在演进（metrics 曾多次破坏性变更） | 中 | 全部 OTel 触点收敛在 libs/obs 单点，业务面只见自家 API；升级影响半径=1 个库 |
| otel-lgtm 镜像体积（~1GB）拖慢首次体验 | 低 | README 标注预拉取；obs-up 独立于 dev-deps-up，不用观测时零成本 |
| redaction 误伤正常业务文本 | 低 | 保守模式白名单式模式匹配（§2.1），每次扩模式需附误伤评估 |
| 日志异步 buffer 在崩溃时丢尾部日志 | 低 | panic 路径同步 flush；权衡记录在 D1 注释，Stage 1 基线验证 |

## §7 DoD

- [ ] D1-D5 就位，T1-T8 全部通过
- [ ] 四个 Go 占位服务与 skills 包全部经 libs/obs 初始化（规则 8 口径达成）
- [ ] label 白名单与禁入清单在代码常量与 spec §2.2 一致
- [ ] .env.example 新增观测配置项（0.7 契约履行）
- [ ] README"本地可观测"一节：obs-up → 三信号查询路径截图
- [ ] redaction 模式清单及测试固化
- [ ] 观测初始化失败降级路径演示（T5 记录）
- [ ] 与审计边界的说明进入文档（§1.2 表述复核）
- [ ] specs/README.md 与 roadmap 进度表更新
- [ ] commit 格式合规，独立 commit 序列

## §8 Q&A

**Q1 Go 日志库：A. 标准库 slog（★推荐） B. zap C. zerolog**
推荐 A：零新依赖（规则 8 最友好）、性能对本量级足够、OTel bridge 官方支持；
B/C 的极致性能优势在"日志量由采集上报主导"的本系统不构成决策因子。

**Q2 Python 日志：A. structlog（★推荐） B. stdlib logging + dictConfig**
推荐 A：结构化是一等公民、processor 链天然放 redaction；B 实现同等能力的配置
复杂度反而更高。新依赖已在 Header 报批。

**Q3 本地观测栈：A. grafana/otel-lgtm 单容器（★推荐） B. 手工 compose 四组件 C. 不建（stdout）**
推荐 A：官方 all-in-one（Loki+Grafana+Tempo+Prometheus+collector），一条命令三信号
可视，维护面≈0；B 是自找的四组件版本矩阵；C 无法满足 0.12"可观测性可见"验收。

**Q4 metrics 出口：A. OTLP 统一推送（★推荐） B. prometheus client + scrape 端点**
推荐 A：三信号单出口单配置，与 tracing 共用 collector；KEDA 所需 PromQL 由
collector 落存储满足。B 多一条 scrape 通道与端口暴露面，语义分裂。

**Q5 采样：A. 配置化 head 采样 + 错误 span 强采（★推荐） B. 恒 100%**
推荐 A：生产 100% 采样的存储成本随会话量线性爆炸；错误强采保住排障主场景。
tail-based 采样列为 Stage 2+ 演进项不预建。

## §9 实施计划

| 步骤 | 内容 | 估时 |
|---|---|---|
| 1 | D1 libs/obs（日志+redaction）+ T2/T3 | 0.5 天 |
| 2 | D1 tracing/metrics + 中间件 + T1/T4/T5/T6 | 0.75 天 |
| 3 | D2 Python + T7；D4 obs 栈 + T8 | 0.5 天 |
| 4 | D5 演示端点 + README + DoD 收尾 | 0.25 天 |

总计 2 天。

## §10 后续 spec 关联

- spec-0.12：验收"三信号可见"直接复用 D5 演示路径；
- spec-1.x 全部服务：接入 libs/obs 为完成定义组成部分（规则 8）；
- spec-1.7（LLM 网关）：model/token 用量指标按 §2.2 白名单扩展（届时修订 label 表）；
- spec-1.15（审计）：与本 spec 的边界声明互为引用；
- spec-1.16：性能基线采集依赖本 spec 指标体系。
