# Skill 运行时设计：跨容器调用与多 agent 复用

> 日期：2026-08-09 · 状态：定稿（AD-3/AD-12 落地）· 随 spec-1.9 细化
> 回答两个问题：agent 与 skill 分容器如何技术实现；同一 skill 容器如何被多个 agent
> 在不同时间（乃至同时）调用。

## 1. 基本模型

**skill 容器 = 无状态 MCP server（streamable HTTP transport）**，以 k8s Deployment +
Service 部署；agent-runtime 内嵌 codexgo MCP client（MCP 2026-07-28 协议，同步簇 A）。

```
 agent-runtime Pod A ──┐                      ┌▶ skill-slowquery  Deployment(×N) + Service
 agent-runtime Pod B ──┼── MCP over HTTP ─────┼▶ skill-inspection Deployment(×N) + Service
 agent-runtime Pod C ──┘   (k8s Service LB)   └▶ skill-compliance CronJob/Job
```

- **发现**：skill 注册表（控制面 PG）记录 `skill_id → MCP endpoint(Service DNS)、版本、
  租户可见性`；agent 挂载 = 注册表关联；工具 schema 由 MCP `tools/list` 自声明并被
  runtime 缓存（利用 0.147 的"启动前暴露缓存工具目录"）；
- **调用**：`tools/call`，输入为已采集的结构化数据引用 + 参数——skill 不碰数据库、
  不持凭据（AD-3/AD-4），需要数据时凭调用票据向数据接入层只读 API 取数。

## 2. 多 agent 复用同一 skill 容器

skill 是普通无状态 HTTP 服务，"被多个 agent 调用"= 多客户端并发请求，无需特殊机制；
需要纪律保证的是**无状态性与隔离**：

1. **请求级隔离**：每次 `tools/call` 自带完整上下文，skill 进程内禁止跨请求共享可变状态
   （允许只读缓存：模型、规则库）；
2. **上下文传递**：`tenant_id / agent_id / session_id / trace_id / 调用票据` 经 HTTP header
   （`X-AiRush-*` + W3C traceparent）传递；skill 日志与下游取数强制携带，形成审计链；
3. **并发与背压**：skill 声明单实例并发上限（注册表字段），超限返回 429，runtime 按
   指数退避重试；横向扩容交给 HPA（见 k8s-scaling-design.md）；
4. **幂等**：`tools/call` 携带 `invocation_id`（UUID7），skill 对同 ID 重复调用返回缓存
   结果（Redis，TTL 1h）——网络重试不产生重复副作用；
5. **超时分层**：runtime 侧每工具 schema 声明超时（快分析 30s / 重分析 5min）；超时的
   长任务应改为 Job 型。

## 3. 两种形态的实现

| | 常驻型 | Job 型 |
|---|---|---|
| 载体 | Deployment + Service | k8s Job（由巡检调度中心/控制面创建） |
| 触发 | agent MCP 直调 | 调度器投递任务消息（Redis Stream 队列） |
| 结果 | MCP 响应同步返回 | 写结果存储 + 事件回调 agent-runtime 继续会话 |
| 适用 | 慢查询分析、巡检报告、基线对比 | 月度合规报告、全租户批量扫描 |
| 冷启动 | 无 | 秒级，可接受（低频重活） |

Job 型对 agent 的抽象仍是 MCP 工具：runtime 提供 `submit_job/query_job` 通用工具，
skill 侧只实现批处理入口——保持"skill 即 MCP 工具"心智统一。

## 4. 安全边界

- skill 命名空间默认 **NetworkPolicy 拒绝出网**，白名单仅开放：数据接入层只读 API、
  LLM 网关（需要 LLM 的分析 skill）、Redis 幂等缓存；
- skill 镜像不含任何凭据；调用票据由 runtime 每次签发（短时效 JWT，scope 限定
  tenant + 数据范围），数据接入层校验；
- skill 输出回 agent 前经尺寸上限与内容审计钩子（防超大输出与数据外带）。

## 5. Skill 开发规范（骨架）

- 语言无关（MCP 协议边界）；平台提供 Python skill 模板（FastMCP + 结构化日志 +
  幂等中间件 + 票据取数 SDK），Go 模板按需；
- 每个 skill 独立目录 `skills/<name>/`、独立镜像、独立版本；注册表登记 schema 版本，
  破坏性变更走新 tool name（`analyze_slowquery_v2`），旧版本保留一个弃用周期。

## 6. 修订历史

| 日期 | 变更 |
|---|---|
| 2026-08-09 | 初版 |
