# spec-0.8 错误码与 API 错误规范

> **approved & shipped** — Stage 0 验收签署 2026-08-10（分级预批流程，spec-0.12）

## Header / 元数据

- **位置**：Stage 0 第 8 个功能点；前置 spec-0.1/0.7（libs/ 模式已立）；被全部对外
  接口类 spec（1.1 API、1.2 Connector 协议、1.9 MCP skill）消费；
- **配套规则**：development-standards §1.3（错误必须处理/传递、用户消息不泄内部细节）、
  §5（API 错误响应 `{code, message, trace_id}`）、§3（Python AirushError 树）；
  CLAUDE.md 规则 4（每个错误码有触发用例）、规则 6（未实现分支显式报错含错误码）；
- **决策日期**：2026-08-09；
- **实施修订（2026-08-10）**：① 注册表为 **proto/errors.json**（非 yaml）——生成器
  纯 stdlib 零依赖，注释以 $comment/description 字段承载；② `AR_INTERNAL` 更名
  `AR_INTERNAL_ERROR`（严格执行 AR_<DOMAIN>_<REASON> 三段格式，生成器校验拦截）；
  ③ D5"占位 HTTP handler 端到端演示"以 httptest 用例实现（占位 main 尚无 HTTP
  服务，spec-0.9 D5 起真实端点后自然衔接）；④ 无新增第三方依赖（生成器 stdlib）。

## §1 范围

### §1.1 包含（Deliverables）

| # | Deliverable | 文件清单 | 估算 | 说明 |
|---|---|---|---|---|
| D1 | 错误码注册表 + 生成器 | `proto/errors.yaml`（SSOT：code/level/http/message 模板/说明）+ `deploy/scripts/gen-errors.go`（生成 Go 与 Python 常量）+ `make generate` | 3 文件 ~180 行 | 生成物按 §0 豁免 lint/覆盖率 |
| D2 | Go 错误库 | `libs/apierror/`：Error 类型（code+wrap 链）、HTTP 映射、`Middleware`（统一转换 + panic recovery→`AR_INTERNAL`）、`errors.Is/As` 兼容 | ~5 文件 ~300 LOC | 四组件共享，使用方 ≥2 成立 |
| D3 | Python 错误树 | `skills/airush_skills/errors.py`：`AirushError` 基类树 + 生成的错误码常量 + MCP 错误响应转换中间件骨架 | ~2 文件 ~120 LOC | skill-runtime-design 的异常出口约定落地 |
| D4 | API 错误响应契约 | 本 spec §2.2 定版 JSON 形态 + OpenAPI 错误 schema 片段（`proto/openapi/error.yaml`，spec-1.1 组装引用） | 1 文件 ~40 行 | 前端类型自此生成 |
| D5 | 初始错误码集 + 触发用例 | 通用域约 15 个码（COMMON/AUTH/TENANT/VALIDATION/INTERNAL/UPSTREAM/QUOTA），每码 ≥1 单测触发 | 用例 ~15 个 | 规则 4 从第一批码起执行 |

### §1.2 不包含

| 不做的事 | 理由 |
|---|---|
| 前端错误展示组件与文案 | spec-1.13/1.14 按 UI 规范做；本 spec 只保证 code 可编程分派 |
| 错误消息 i18n | 产品当前中文单语；注册表 message 字段单语，未来加列即可扩展 |
| 错误率 metrics 与告警 | spec-0.9 承载；本 spec 仅定 metrics label 用 `code` 的约定 |
| 各域业务错误码全集 | 域码由引入方 spec 增量注册（如 1.2 注册 CONNECTOR 域），本 spec 只给通用域 |
| 客户端重试策略 | 属调用方行为契约，各接口 spec 按 §2.3 分级自行声明 |
| gRPC status 映射 | 首个 gRPC 接口（spec-1.2）出现时在该 spec 补映射表，避免无实物空设计 |

### §1.3 例外说明

无偏离。`proto/` 目录（spec-0.1 预留）由本 spec 首次启用——errors.yaml 属跨服务
契约，符合该目录"唯一契约来源"定位。

## §2 接口设计

### §2.1 错误码形态（定版）

- 格式：`AR_<DOMAIN>_<REASON>` 全大写蛇形，如 `AR_TENANT_NOT_FOUND`、`AR_VALIDATION_FAILED`、
  `AR_UPSTREAM_LLM_TIMEOUT`；
- 立码原则：**码 = 调用方可作出不同处置的区分**；仅日志排障用的细节进 message/wrap 链，
  不立码（防码爆炸）；
- 域清单由注册表 `domains:` 段收口，新域随引入 spec 注册。

### §2.2 API 错误响应（定版）

```json
{
  "code": "AR_VALIDATION_FAILED",
  "message": "请求参数不合法",
  "trace_id": "tr_9f2ae1c0",
  "details": [ { "field": "port", "reason": "必须在 1-65535 之间" } ]
}
```

- `message` 面向用户：模板来自注册表，禁运行时拼接内部细节（SQL/路径/堆栈）；
- `details` 可选，结构化数组（§8 Q3），仅 E1 验证类使用；
- `trace_id` 必填，与日志/审计贯通（spec-0.9 约定一致）。

### §2.3 错误分级（定版）

| 级 | 语义 | HTTP | 例 |
|---|---|---|---|
| E1 | 用户可修复（输入/状态） | 400/422 | VALIDATION_FAILED |
| E2 | 认证/授权 | 401/403 | AUTH_TOKEN_EXPIRED |
| E3 | 资源不存在/冲突 | 404/409 | TENANT_NOT_FOUND |
| E4 | 平台内部缺陷 | 500 | INTERNAL |
| E5 | 上游依赖故障 | 502/503 | UPSTREAM_LLM_TIMEOUT、CONNECTOR_OFFLINE |
| E6 | 配额/限流 | 429 | QUOTA_EXCEEDED |

每码必属一级；级决定 HTTP 状态与（未来）告警权重，注册表中为必填字段。

## §3 行为契约

- 任何 handler 返回的 error 若非 apierror 类型，中间件转 `AR_INTERNAL`（E4）并告警级日志——
  "忘了立码"表现为可见的 500 而非静默泄漏内部错误文本；
- panic → recovery → `AR_INTERNAL` + trace_id，响应体不含栈（栈进日志）；
- 错误码一经合并 main 即不可删除/改语义（客户端兼容契约）；弃用走注册表 `deprecated: true`
  并保留至少一个大版本；
- 生成物与 errors.yaml 不一致时 CI 失败（`make generate` 后 git diff 非空即红）。

## §4 测试用例

| # | 用例 | 目的 |
|---|---|---|
| T1 | 每个初始码触发一次并断言 code/http/level（15 例） | 规则 4 基线 |
| T2 | 未注册裸 error 经中间件 → AR_INTERNAL + 告警日志 | 兜底路径 |
| T3 | panic → 500 响应无栈、日志有栈有 trace_id | 泄漏防护 |
| T4 | message 模板含内部占位符时生成器报错 | 防泄漏前移 |
| T5 | errors.yaml 改动未跑 generate → CI diff 检查红 | SSOT 一致性 |
| T6 | Python AirushError→MCP 错误响应含 code | 双语言语义一致 |
| T7 | errors.Is/As 穿透 apierror wrap 链 | Go 惯用法兼容 |
| T8 | 删除已存在码 → 注册表 diff 检查报错 | 不可删除契约 |

## §5 与现有代码的 contract

- 新增：proto/errors.yaml、生成器、libs/apierror、Python errors、OpenAPI 片段；
- 修改：Makefile（generate 目标）、CI（生成物一致性检查步骤）；
- 不动：现有占位服务逻辑（接入在各业务 spec）；
- 对后续 spec 的接口：§2.1 立码原则、§2.2 响应形态、§2.3 分级表、"引入码的 spec
  必须在其 §4 提供触发用例"——四者为硬约定。

## §6 风险

| 风险 | 概率 | 缓解 |
|---|---|---|
| 错误码粒度失控（每函数一码/一域塞百码） | 中 | §2.1 立码原则入 review checklist；单 PR 新增 >5 码需在描述中逐个论证 |
| message 被运行时拼接泄内部细节 | 中 | 模板占位符白名单（T4 生成期拦截）+ review；details 结构化通道给合法动态内容 |
| 双语言常量漂移 | 低 | 单 SSOT 生成 + T5 CI 检查，漂移不可能静默存在 |
| 生成器自身成为维护点 | 低 | 纯文本模板 ~100 LOC 无依赖；坏了手写常量也能应急（格式稳定） |
| E5 上游错误被误标 E4 污染告警信号 | 中 | 分级 review 表（注册表 PR 必看字段）；0.9 metrics 按 level 分维度后可观测纠偏 |

## §7 DoD

- [ ] D1-D5 就位，T1-T8 全部通过
- [ ] `make generate` 幂等且生成物入库（豁免 lint/覆盖率已配置）
- [ ] 初始 15 码每码有触发用例（清单附 PR）
- [ ] 中间件在一个占位 HTTP handler 上端到端演示（请求→错误响应 JSON）
- [ ] Python MCP 转换骨架被 skills 冒烟用例覆盖
- [ ] OpenAPI 错误片段通过 spectral/swagger 校验
- [ ] 注册表含 deprecated 机制且有说明
- [ ] development-standards §1.3/§5 与本 spec 复核无矛盾
- [ ] specs/README.md 与 roadmap 进度表更新
- [ ] commit 格式合规，独立 commit 序列

## §8 Q&A

**Q1 码形态：A. 符号字符串 AR_DOMAIN_REASON（★推荐） B. 数字码段（AR10401）**
推荐 A：自描述（日志/审计/对话中直接可读）、无需查表、grep 友好；数字码的
"节省字节/分段管理"收益在本产品量级不存在，且 LLM 生成的诊断文本引用符号码更自然。

**Q2 注册表：A. errors.yaml SSOT + 生成三语言常量（★推荐） B. Go 包手写为 SSOT，其他语言人肉同步**
推荐 A：双语言 + 前端三处消费，人肉同步必漂移；生成器极薄且 T5 保证一致性闭环。

**Q3 details 形态：A. 结构化数组 field/reason（★推荐） B. 自由 map**
推荐 A：前端可编程渲染（表单逐字段标错）、OpenAPI 可精确建 schema；自由 map
必然演化成各接口私有约定。

**Q4 panic 出口：A. recovery→AR_INTERNAL，栈只进日志（★推荐） B. 默认 500 文本**
推荐 A：响应含栈 = 内部结构泄漏（安全）；统一出口保证 trace_id 必达，排障不降级。

**Q5 分级模型：A. 六级 E1-E6（★推荐） B. 4xx/5xx 二分**
推荐 A：E5（上游）与 E4（自身缺陷）的区分直接决定告警去向与 SLO 归责——对依赖
LLM/Connector 的本产品是刚需；二分模型迟早被迫重分类，届时是破坏性变更。

## §9 实施计划

| 步骤 | 内容 | 估时 |
|---|---|---|
| 1 | D1 注册表 + 生成器 + T4/T5/T8 | 0.5 天 |
| 2 | D2 Go 库 + 中间件 + T1/T2/T3/T7 | 0.75 天 |
| 3 | D3 Python 树 + T6；D4 OpenAPI 片段 | 0.5 天 |
| 4 | DoD 收尾 | 0.25 天 |

总计 2 天。

## §10 后续 spec 关联

- spec-0.9：日志/metrics 以 code+level 为标准 label；
- spec-1.1：API 骨架全量接入中间件，OpenAPI 引用 D4 片段；
- spec-1.2：注册 CONNECTOR 域 + gRPC status 映射表；
- spec-1.9：MCP 错误通道采用 D3 转换语义；
- 规则 6 执行面：未实现分支一律 `AR_COMMON_NOT_IMPLEMENTED`（初始码集包含）。
