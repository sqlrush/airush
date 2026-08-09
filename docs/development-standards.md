# 详细开发规范

> 日期：2026-08-09 · 状态：定稿（工具链配置细节随 spec-0.2 落地）
> 层级：全局规则（~/.claude/rules/common/*.md）< 本文 < CLAUDE.md 研发纪律。
> 本文管"代码怎么写"，CLAUDE.md 管"流程怎么走"。

## 1. 通用铁律（三语言一致）

1. **不可变优先**：不原地修改传入对象；更新返回新副本（全局 coding-style 同款，review 必查）；
2. **边界校验**：所有外部输入（API 请求、Connector 上报、MCP 调用参数、LLM 输出）
   进入业务逻辑前必须 schema 校验，fail fast；**LLM 输出视为不可信输入**；
3. **错误处理**：错误必须处理或显式向上传递并附加上下文；禁止吞错、禁止裸 catch/recover；
   面向用户的错误消息不泄漏内部细节（错误码机制见 spec-0.8）；
4. **尺寸约束**：函数 <50 行、文件 <800 行（目标 200-400）、嵌套 ≤4 层；超限即拆；
5. **日志**：结构化 JSON，必带字段 `tenant_id / trace_id / component`（无租户上下文的
   系统日志显式标 `tenant_id: "-"`）；禁止打印凭据、令牌、客户数据原文；
6. **命名**：域词汇统一用设计文档词表（tenant/agent/skill/connector/datasource/
   inspection/session/turn），禁止同义词混用（如 client/customer 混指租户）。

## 2. Go 规范（console / gateway / connector / agent-runtime）

- 版本 Go 1.23+；golangci-lint 配置为准（spec-0.2），必开：errcheck、govet、staticcheck、
  depguard（解耦边界，见 decoupling-architecture.md §2.2）、gosec；
- `context.Context` 贯穿所有阻塞调用第一参数；租户上下文经自定义 ctx key 类型传递，
  提供 `tenancy.FromContext(ctx)` 唯一取用点；
- 错误：`fmt.Errorf("...: %w", err)` 包装；哨兵错误用 `errors.Is/As`；对外错误转 spec-0.8 错误码；
- 包布局：`cmd/<binary>/main.go` 只做装配；业务在 `internal/<domain>/`；禁止 `internal/util` 垃圾抽屉；
- 并发：goroutine 必须有明确生命周期归属（errgroup / 显式 cancel）；共享状态优先 channel，
  用锁必须注释保护不变量；
- 测试：表驱动 + `t.Parallel()` 默认；集成测试 build tag `//go:build integration`。

## 3. Python 规范（skills / 记忆服务）

- 版本 3.12+；uv 管理；ruff（含 isort/pyupgrade 规则集）+ mypy strict 全通过；
- 全量类型注解；数据边界一律 pydantic v2 模型（禁裸 dict 穿越模块边界）；
- 异步优先（FastAPI/FastMCP 栈）；同步重计算放线程池，禁止阻塞事件循环；
- 异常：自定义异常树基于 `AirushError`；skill 入口统一异常→MCP 错误响应中间件；
- 测试：pytest + pytest-asyncio；fixture 不做隐式全局状态。

## 4. TypeScript/React 规范（frontend）

- Node 22+ / pnpm；eslint（typescript-eslint strict）+ prettier；`noImplicitAny`、
  `strictNullChecks` 开启，禁 `any`（例外须 `// eslint-disable` 带理由）；
- 组件函数式 + hooks；服务端状态用 TanStack Query，禁止手写 fetch-in-useEffect；
- API 类型从 OpenAPI 生成（契约唯一来源），禁止手写响应类型；
- 目录按 feature 组织（`features/inspection/`），共享组件进 `components/` 需 ≥2 使用方。

## 5. API 设计约定（控制面 REST）

- 路径：`/api/v1/<资源复数>`；租户从认证态推导，**永不出现在路径/参数中**（防越权遍历）；
- 分页：cursor-based（`?cursor=&limit=`），响应 `{items, next_cursor}`；
- 错误响应统一 `{code, message, trace_id}`（spec-0.8）；HTTP 语义正确（4xx 客户端/5xx 服务端）；
- 变更类接口幂等：`Idempotency-Key` header 支持；
- 破坏性 API 变更走 `/v2`，`/v1` 保留一个弃用周期并打 `Deprecation` header。

## 6. Git 与 PR

- 分支：`feat/spec-1.2-connector-core` 式命名（type/spec 号-slug）；main 受保护；
- commit：`<type>: <description>`，单 commit 单意图；spec 实装期允许 WIP commit，
  合并前 squash 成有意义序列；
- PR：描述含 spec 链接 + DoD 勾选状态 + 测试证据（输出粘贴或 CI 链接）；
  安全敏感改动（凭据/租户/审批路径）必须额外过 security review；
- review 清单：本文 §1 铁律 + CLAUDE.md 红线 + spec §5 contract 逐条核对。

## 7. 修订历史

| 日期 | 变更 |
|---|---|
| 2026-08-09 | 初版 |
