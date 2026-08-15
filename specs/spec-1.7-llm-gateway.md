# spec-1.7 LLM 网关

> **frozen** — user approve 2026-08-15（§8 Q1-Q7 **全采 ★**；新增第三方镜像 `ghcr.io/berriai/litellm` 一并批准；`libs/tenancy` 提取一并批准；无新增 Go module）。
> 起草前已就 LiteLLM 形态做过实测（`deploy/scripts/probe-litellm/`），§2/§6 中标 **实测** 的
> 断言来自那次探测，不是推演。

## Header / 元数据

- **位置**：Stage 1 **框架组第一件**（2026-08-15 顺序修订：框架组 1.7 → 1.8 → 1.9 →
  记忆/知识库组 1.18-1.20 → 数据库功能组）。采集组 1.1-1.5 已 shipped，本 spec 不依赖它们；
  被 **spec-1.8 Agent Runtime** 直接消费（agent loop 的 LLM 出口），被 spec-1.20 知识库
  （embedding 路由复用）、spec-1.15 审计（用量事件）、spec-2.8（租户限流）间接消费；
- **上游决策**：**AD-8**（独立 LLM 网关，LiteLLM 起步；agent 直连供应商否决——成本与限流失控）；
  roadmap §0.3 #5（**不做计费**：用量统计仅用于成本观测与配额，不进入账单链路）；
  AD-3（网关经手的 prompt 只含脱敏/规范化后的结构化数据，网关**不得持久化** prompt）；
  AD-1（agent 无状态：配额/用量状态外置控制面 PG，不在 runtime 进程里）；
- **核心定位**：给 agent-runtime 一个 **OpenAI 兼容、多供应商、可换后端**的 LLM 出口，
  同时把"谁、用了多少、还剩多少"记在**平台侧**（控制面 PG，RLS），LiteLLM 只做**无状态路由**——
  这样 LiteLLM 是可替换件：Stage 4 私有化打包若嫌 Python 进程重，换 Bifrost/自研 Go 网关时
  上层零改动（解耦架构文档"可替换点表"登记，见 §5）；
- **依赖审批（规则 5 硬门槛 #4）**：**新增一个第三方容器镜像**
  `ghcr.io/berriai/litellm:main-stable`（实测版本 **1.96.2**，镜像 2026-08-11 构建，381 MB；
  实施第 1 步钉 digest）——这是 AD-8 既定选型的落地，非新决策，但作为可部署组件须在此登记。
  **无新增 Go module**（网关客户端用 `net/http` + `encoding/json` 手写，~550 LOC；不引 openai-go
  SDK：它会把 OpenAI 的类型体系拖进 libs，而调用方 codexgo 有自己的客户端）。
  **无 Python 代码进本仓库**（LiteLLM 是现成镜像）。dev 环境额外一个**我们自己**的 mock 供应商
  镜像（Go，`deploy/scripts/probe-litellm/mockllm.go` 演进而来）；
- **实测支撑**（2026-08-15，`probe-litellm/run.sh`、`run2.sh`）：

  | 断言 | 结果 |
  |---|---|
  | 无 `DATABASE_URL` 可跑（无状态形态） | ✅ readiness `{"status":"healthy","db":"Not connected"}` |
  | master key 强制 | ✅ 无 key `/v1/models` 401 |
  | Chat Completions 非流式/流式透传，usage 到手 | ✅ 流式在 `stream_options.include_usage=true` 下末帧带 usage |
  | **Responses API → chat-only 供应商** | `deepseek/` 前缀 **✅ 桥接**（LiteLLM 转成 chat 再打后端）；`openai/` 前缀 **❌ 原样透传** `/v1/responses` → 后端 404 |
  | `/metrics`（Prometheus） | 需 `litellm_settings.callbacks: ["prometheus"]`；在 `/metrics/`（`/metrics` 307）；**须带 master key**（无 key 401）|
  | 成功路径日志是否含 prompt | `json_logs: true` 下 canary 出现 **0 次** |
  | 未知模型 | 400，消息含模型名与"Call /v1/models"，不含 key |

- **决策日期**：2026-08-15 起草并 approve（§8 全 ★）。

---

## §1 范围

### §1.1 包含（Deliverables）

| # | Deliverable | 文件清单 | 估算 | 说明 |
|---|---|---|---|---|
| **D1** | LiteLLM 部署 | `deploy/charts/airush/templates/llm-gateway.yaml`（新）、`values.yaml`/`values-dev.yaml`（改）、`templates/_helpers.tpl`（改：master key Secret lookup 复用） | ~240 LOC | Deployment + ClusterIP Service + ConfigMap（模型路由）+ Secret（master key + 供应商 key）；探针、requests/limits、PDB；`DISABLE_ADMIN_UI`、telemetry off、json_logs；dev 形态带 mock 供应商 sidecar |
| **D2** | 配额与用量 schema | `console/migrations/0005_llm_quota_usage.up.sql` / `.down.sql` | ~150 LOC | `llm_quotas`（租户月度 token 预算）+ `llm_usage`（每次调用一行）；标准 RLS 模板；月分区/保留在 §8 Q4 |
| **D3** | 网关客户端库 `libs/llm` | `libs/llm/{client.go,meter.go,usage.go,quota.go,errors.go}`（新） | ~550 LOC | `Meter` = `http.RoundTripper` 中间件：注入租户/agent/会话/trace 头、流式自动加 `include_usage`、从 chat/responses × 流式/非流式四种响应里提取 usage、调用前配额门、调用后记账；错误映射到 AR_LLM_*；调用方（codexgo 客户端）零改动挂上 |
| **D4** | 控制面配额/用量面 | `console/internal/repo/llm.go`（新）、`console/internal/svcapi/llm.go`（新：内部 API 供 runtime 记账/查额）、`console/internal/httpapi/llm.go`（新：公开 API 配额 CRUD + 用量聚合）、`console/cmd/console/server.go`（改） | ~420 LOC | 配额存取、用量写入、按天/按模型聚合；Stage 1 租户来自默认租户中间件 |
| **D5** | 观测与错误码 | `libs/llm/obs.go`、`proto/errors.json`（改，+3 码）| ~110 LOC | `airush_llm_requests_total{model,status,code}`、`airush_llm_tokens_total{model}`、`airush_llm_request_duration_ms{model}`；LiteLLM `/metrics` 抓取说明；错误码 `AR_LLM_QUOTA_EXCEEDED`/`AR_LLM_UPSTREAM_FAILED`/`AR_LLM_MODEL_UNKNOWN` |
| **D6** | 测试与验证 | `libs/llm/*_test.go`、`libs/llm/llm_integration_test.go`（testcontainers 起真 LiteLLM + 进程内 mock 供应商）、`console/internal/{repo,svcapi,httpapi}/*llm*_test.go`、`deploy/scripts/dev-verify.sh`（改）、`deploy/scripts/probe-litellm/`（已存在，演进为 mock 镜像源码） | ~760 LOC | 单元 T1-T10、集成 T11-T18、端到端 T19-T21 |

合计估算 ~2230 LOC。

### §1.2 不包含（每条带理由）

| # | 不包含 | 理由 |
|---|---|---|
| 1 | 计费 / 账单 / 按金额扣费 | roadmap §0.3 #5 user 定不做计费；本 spec 只记 token 与（若网关返回）参考成本，不做定价表、不做扣费 |
| 2 | LiteLLM 内置的虚拟 key / 团队 / 预算 / spend log（即给它挂 PG） | 会造成配额**两份事实源**（LiteLLM 库一份、控制面一份）；且 LiteLLM 有状态后不能随意扩缩、prompt 会进它的 spend log（AD-3）。§8 Q1 |
| 3 | 响应缓存（LiteLLM Redis cache） | 诊断对话上下文各异，命中率低；缓存跨租户共享有隔离风险（同 prompt 不同租户命中同一答案本身不泄漏，但缓存内容含租户脱敏数据，落 Redis 需另定隔离与保留）。Stage 2 按需评估 |
| 4 | 模型目录可在控制台配置（DB 表 + UI） | Stage 1 只有默认租户、模型集合由部署方决定，Helm ConfigMap 够用；租户级模型可见性/偏好归 Stage 2（spec-2.8 一并做租户维度配置）。§8 Q6 |
| 5 | 每租户 RPS / 并发限流 | 归 spec-2.8 速率限制与租户配额；本 spec 只做**月度 token 预算**这一种配额（成本护栏），并发公平性由 1.8 会话调度器 + 2.8 处理 |
| 6 | 本地模型接入（vLLM / Ollama） | 归 spec-4.5 私有化打包（离线 embedding/chat）；LiteLLM 原生支持 `hosted_vllm/`、`ollama/` 前缀，届时只加 ConfigMap 条目，本 spec 结构不变 |
| 7 | embedding 端点的路由与配额 | 归 spec-1.20 知识库（embedding 服务形态在那里定：独立服务 vs 经 LiteLLM `/v1/embeddings`）；本 spec 的 `Meter` 对 `/v1/embeddings` 响应的 usage 提取**预留分支但不验收** |
| 8 | prompt / 响应内容审计留存 | 归 spec-1.15 审计（那里只留元数据：谁、何时、哪个模型、多少 token、trace_id）；内容留存与 AD-3 冲突，任何 Stage 都不做 |

### §1.3 例外说明

无。前置 spec-0.6（迁移）/0.7（配置）/0.8（错误码）/0.9（观测）/0.10（Helm）均 shipped。
**顺序例外**：本 spec 按 2026-08-15 roadmap 修订排在 1.6 之前（1.6 已移出 MVP），非跨 Stage。

---

## §2 接口设计

### §2.1 部署形态全景

```
                     ┌──────────────── k8s（Helm release）────────────────┐
 agent-runtime ──────┤  Service airush-llm (ClusterIP :4000)              │
 (spec-1.8, codexgo  │    └─ Deployment airush-llm ×N（无状态，可 HPA）      │
  客户端 + Meter)    │         ├─ litellm 容器（--config /app/config.yaml） │
        │           │         │     ConfigMap: 模型路由；Secret: keys        │
        │           │         └─ [dev] mockllm sidecar（假供应商，:18099）  │
        │           └────────────────────────────────────────────────────┘
        │  每次调用前后：/internal/v1/llm/{quota-check,usage}（svc token）
        └──────────▶ console（控制面 PG：llm_quotas / llm_usage，RLS）
                          └─ 公开 API：/api/v1/llm/quota、/api/v1/llm/usage
```

三条边界：
- **LiteLLM 只见得到 prompt，见不到租户/用量**：它没有 DB、不知道谁是谁（请求头里的租户/agent 标识仅供它的结构化日志关联 trace）；
- **控制面只见得到用量，见不到 prompt**：记账请求只带 token 数与元数据；
- **agent-runtime 是唯一同时见到两者的地方**——`Meter` 就在那个进程里跑，与 AD-1"状态外置"一致：它自身不存任何东西。

### §2.2 D1：Helm 值面

```yaml
llm:
  enabled: true
  replicas: 1
  image: ghcr.io/berriai/litellm:main-stable   # 实施时改为 @sha256 digest 钉版（1.96.2）
  resources: { requests: {cpu: 200m, memory: 512Mi}, limits: {cpu: "1", memory: 1Gi} }
  masterKeySecret: ""        # 生产：引用既有 Secret（key: master-key）；空 = chart 生成并 lookup 复用
  providerKeysSecret: ""     # 生产：引用既有 Secret（键名 = 供应商环境变量名，如 DEEPSEEK_API_KEY）
  models:                    # 渲染进 ConfigMap 的 model_list；逻辑名是平台侧稳定契约
    - name: chat-default     # agent 默认模型（1.8 的 agent 配置引用它）
      litellm: deepseek/deepseek-chat
      apiKeyEnv: DEEPSEEK_API_KEY
    - name: chat-strong
      litellm: anthropic/claude-sonnet-4-5
      apiKeyEnv: ANTHROPIC_API_KEY
  fallbacks:
    chat-default: [chat-strong]
  mockProvider:              # 仅 values-dev：sidecar 假供应商，models 全指向它
    enabled: false
```

渲染规则（写进 `_helpers.tpl`）：
- `litellm_settings`：`telemetry: false`、`json_logs: true`、`drop_params: true`、`callbacks: ["prometheus"]`、
  `fallbacks` 由 values 生成；`general_settings`：`master_key: os.environ/LITELLM_MASTER_KEY`；
- 环境变量 `DISABLE_ADMIN_UI=True`（管理 UI 不进产品面）；供应商 key 全部走 `os.environ/<NAME>` 引用，
  ConfigMap 里**不出现任何 key 明文**；
- **供应商前缀规则**（实测结论，写进模板注释）：chat-only 供应商必须用其**原生前缀**（`deepseek/`、
  `hosted_vllm/`…）而非 `openai/` + `api_base`——前者 LiteLLM 会把 Responses API 桥接成 chat，
  后者原样透传 `/v1/responses` 到一个不认识它的后端；无原生前缀的供应商见 §8 Q3 备选；
- 探针：liveness `/health/liveliness`，readiness `/health/readiness`；PDB `maxUnavailable: 1`；
- Service 仅 ClusterIP，无 Ingress；NetworkPolicy 归 Stage 2。

### §2.3 D2：schema

```sql
-- 0005_llm_quota_usage（标准 RLS 模板四要素，spec-0.6 §2.2）
CREATE TABLE llm_quotas (
    tenant_id      uuid        NOT NULL REFERENCES tenants(id),
    period         text        NOT NULL DEFAULT 'monthly' CHECK (period IN ('monthly')),
    token_budget   bigint      NOT NULL CHECK (token_budget >= 0),   -- 0 = 禁用 LLM
    hard_stop      boolean     NOT NULL DEFAULT true,               -- 超额拒绝 vs 仅告警
    updated_at     timestamptz NOT NULL DEFAULT now(),
    PRIMARY KEY (tenant_id, period)
);

CREATE TABLE llm_usage (
    tenant_id          uuid        NOT NULL REFERENCES tenants(id),
    id                 uuid        NOT NULL DEFAULT gen_random_uuid(),
    at                 timestamptz NOT NULL DEFAULT now(),
    model              text        NOT NULL,      -- 平台逻辑名（chat-default），非供应商名
    upstream_model     text        NOT NULL DEFAULT '',  -- 网关实际命中的后端（fallback 后可能不同）
    agent_id           uuid,                       -- 1.8 起填；Stage 1 允许 NULL
    session_id         text        NOT NULL DEFAULT '',
    trace_id           text        NOT NULL DEFAULT '',
    purpose            text        NOT NULL DEFAULT '',  -- 'chat' | 'inspection' | 'embedding'…（自由文本，白名单在 1.8/1.9 收口）
    prompt_tokens      integer     NOT NULL,
    completion_tokens  integer     NOT NULL,
    total_tokens       integer     NOT NULL,
    cost_ref_micro     bigint,                     -- 网关回的参考成本（微美元），可空；不进计费
    stream             boolean     NOT NULL DEFAULT false,
    status             text        NOT NULL CHECK (status IN ('ok','upstream_error','quota_rejected','aborted')),
    PRIMARY KEY (tenant_id, id)
);
CREATE INDEX llm_usage_tenant_at_idx ON llm_usage (tenant_id, at DESC);
CREATE INDEX llm_usage_month_idx     ON llm_usage (tenant_id, date_trunc('month', at), model);
-- 两表 ENABLE/FORCE RLS + tenant_isolation policy + GRANT 给 airush_app（同 0002 模板）
```

**为什么 `llm_usage` 一次调用一行而不是只存日聚合**：审计（1.15）要能追到"这一次对话花了多少"，
配额要能精确到当月已用；日聚合丢了 trace 关联。体量：Stage 1 单租户每天几千次调用，一年
百万行量级，普通表 + 索引够用；月分区/保留归 §8 Q4。

### §2.4 D3：`libs/llm` 接口

```go
// Meter 是挂在任意 OpenAI 兼容 HTTP 客户端上的 RoundTripper：
//   请求前 → 注入租户头 + 流式请求自动补 stream_options.include_usage + 配额门（QuotaGate.Check）
//   响应后 → 从 chat/responses × 流式/非流式 提取 usage → Recorder.Record（异步，失败只记日志与指标）
// 调用方（codexgo 的 api client）只需把 http.Client.Transport 换成它——零侵入。
type Meter struct{ /* Next http.RoundTripper; Gate QuotaGate; Rec Recorder; Logger; clock */ }
func NewMeter(next http.RoundTripper, gate QuotaGate, rec Recorder, opts ...Option) *Meter

// 每次调用的归属从 ctx 取：tenancy.FromContext + CallInfo（agent/session/purpose）。
type CallInfo struct{ AgentID, SessionID, Purpose string }
func WithCallInfo(ctx context.Context, ci CallInfo) context.Context

// Usage 是四种响应形态统一后的用量。
type Usage struct{ Model, UpstreamModel string; PromptTokens, CompletionTokens, TotalTokens int; CostRefMicro *int64; Stream bool }

// QuotaGate 在调用前回答"还能不能调"。实现：consoleQuotaGate（经 svcapi 内部 API）。
type QuotaGate interface{ Check(ctx context.Context, tenantID string) error } // 超额 → AR_LLM_QUOTA_EXCEEDED
// Recorder 记一次用量。实现：consoleRecorder（经 svcapi 内部 API，带重试队列）。
type Recorder interface{ Record(ctx context.Context, tenantID string, u Usage, status string) error }

// Client 是给非 codexgo 调用方（console 自检、测试）的最小 chat 客户端；不是 agent 的主路径。
func NewClient(baseURL, masterKey string, transport http.RoundTripper) *Client
func (c *Client) ChatCompletion(ctx context.Context, req ChatRequest) (ChatResponse, error)
```

**usage 提取规则**（`usage.go`，四个分支各有单测）：

| 线协议 | 非流式 | 流式 |
|---|---|---|
| Chat Completions | 顶层 `usage{prompt_tokens,completion_tokens,total_tokens}` | SSE 末帧 `usage`（Meter 已强制 `include_usage`）；未收到末帧即断流 → `status=aborted`，token 按 0 记但**计一次调用** |
| Responses | 顶层 `usage{input_tokens,output_tokens,total_tokens}` → 映射到同一 `Usage` | `response.completed` 事件的 `response.usage`；断流同上 |

参考成本：若响应头 `x-litellm-response-cost` 存在则填 `CostRefMicro`（实施第 1 步实测该头是否存在，
不存在则该列恒空——**不阻塞**）。

### §2.5 D4：控制面 API

内部（svc token，供 agent-runtime）：

| 方法 | 路径 | 语义 |
|---|---|---|
| POST | `/internal/v1/llm/quota-check` | `{tenant_id}` → 200 `{remaining_tokens, budget}` / 429 `AR_LLM_QUOTA_EXCEEDED`（hard_stop 且已用 ≥ 预算）/ 无配额行 = 不限（Stage 1 默认租户由 seed 给一条） |
| POST | `/internal/v1/llm/usage` | `{tenant_id, usage{...}, status}` → 202；幂等键 = `(tenant_id, trace_id, seq)`（Meter 生成），重复上报不双记 |

公开（默认租户中间件，Stage 1）：

| 方法 | 路径 | 语义 |
|---|---|---|
| GET | `/api/v1/llm/quota` | 当前月度预算与已用 |
| PUT | `/api/v1/llm/quota` | `{token_budget, hard_stop}`；预算下调到已用之下即刻生效（下次调用被拒） |
| GET | `/api/v1/llm/usage?from&to&group_by=day\|model` | 聚合：tokens、calls、失败数；窗口护栏同 spec-1.5 |

### §2.6 配置项

| 变量 | 组件 | 说明 |
|---|---|---|
| `AIRUSH_CONSOLE_LLM_DEFAULT_TOKEN_BUDGET` | console | 新租户/默认租户的月度预算 seed（默认 `50000000`，5 千万 token；0 = 禁用） |
| `AIRUSH_AGENT_LLM_URL` / `AIRUSH_AGENT_LLM_KEY` | agent-runtime（1.8 起用） | 网关地址（`http://airush-llm:4000`）与 master key（Secret 注入，`secret:"true"`） |
| `LITELLM_MASTER_KEY` + 各 `<PROVIDER>_API_KEY` | llm Pod | 全部来自 Secret；ConfigMap 只放 `os.environ/` 引用 |

---

## §3 行为契约

1. **配额门 fail-open 还是 fail-closed**：console 内部 API 不可达时，Meter **放行并记指标**
   `airush_llm_quota_check_failed_total`（fail-open）。理由：配额是成本护栏不是安全边界，
   控制面抖动不该让全部 agent 停摆；连续放行超过 N 次进入告警（§6 R5）。**用量记账**则重试
   3 次后落本地日志（结构化、无 prompt），保证可事后补账；
2. **超额语义**：`hard_stop=true` 且 `used >= budget` → 拒；已在飞的调用不中断；单次调用允许
   把用量顶过预算（无法预知 completion 长度）。`hard_stop=false` 只记指标不拒；
3. **流式断流**：客户端取消/网络断 → `status=aborted`，token 记 0，调用计数 +1；不猜测用量；
4. **错误映射**（`errors.go`）：LiteLLM 4xx 中"Invalid model name" → `AR_LLM_MODEL_UNKNOWN`（400）；
   上游 5xx / 连接失败 / fallback 全败 → `AR_LLM_UPSTREAM_FAILED`（502）；**上游错误正文不透传给
   最终用户**（可能含 api_base、供应商内部信息），只进日志；
5. **头注入**：`x-airush-tenant`、`x-airush-agent`、`x-airush-session`、`x-airush-trace`（供 LiteLLM
   json 日志关联）；**不**注入任何用户内容；`Authorization: Bearer <master key>` 由 Meter 统一加，
   调用方代码不接触 key；
6. **无状态**：LiteLLM 多副本各自独立，fallback 决策每副本本地；不引 Redis 做路由状态（Stage 2 视需要）；
7. **兼容性**：`llm_quotas/llm_usage` 是新增表，不动既有 schema；内部 API 新增两条路径；
   公开 API 新增 `/api/v1/llm/*`，OpenAPI 契约同步（`proto/openapi/console.yaml`）；
   `libs/llm` 是新包，无既有调用方。

---

## §4 测试用例

### 单元（`libs/llm`，httptest 假网关，无容器）

| # | 用例 | 目的 |
|---|---|---|
| T1 | chat 非流式响应 → Usage 三个 token 数正确 | 提取分支 ① |
| T2 | chat 流式：请求无 `stream_options` → Meter 自动补 `include_usage`；末帧 usage 被提取 | 提取分支 ②，且不改调用方 |
| T3 | responses 非流式：`input_tokens/output_tokens` 映射到 Usage | 提取分支 ③ |
| T4 | responses 流式：`response.completed` 事件提取 | 提取分支 ④ |
| T5 | 流式中途断开 → Record 收到 `status=aborted`、tokens=0 | 断流语义 |
| T6 | QuotaGate 返回超额 → 请求**不发往**网关，返回 AR_LLM_QUOTA_EXCEEDED | 配额门在前 |
| T7 | QuotaGate 网络错误 → 放行 + 计数指标 | fail-open 语义 |
| T8 | 头注入齐全且 body 未被改写（除 include_usage）；Authorization 由 Meter 加 | 零侵入 |
| T9 | 上游 5xx / "Invalid model name" / 连接拒绝 → 三个错误码；错误正文不进返回值 | 错误映射与不泄漏 |
| T10 | Recorder 失败重试 3 次后落结构化日志，日志无 prompt | 记账可靠性 |

### 集成（testcontainers：真 LiteLLM 容器 + 进程内 mockllm，`//go:build integration`）

| # | 用例 | 目的 |
|---|---|---|
| T11 | 经 LiteLLM 打 chat（非流式/流式）到 mock，Meter 拿到 usage 与 mock 发出的一致 | 全链路透传 |
| T12 | Responses API 经 `deepseek/` 前缀模型 → 桥接成功；经 `openai/` 前缀 → 404 被映射为 UPSTREAM_FAILED | 前缀规则固化（实测结论进用例） |
| T13 | 主模型指向会 5xx 的 mock 模型名，fallback 到备用 → 成功且 `upstream_model` 记为备用 | fallback 与记账 |
| T14 | 未知模型 → AR_LLM_MODEL_UNKNOWN | 错误映射 |
| T15 | LiteLLM `/metrics/` 带 key 200、无 key 401；含 `litellm_total_tokens_metric_total` | 观测可用 + 抓取需鉴权 |
| T16 | LiteLLM 容器 json 日志在成功路径**不含** canary prompt | AD-3 |
| T17 | 0005 up→down→up 幂等；两表 RLS 四要素；跨租户不可见 | schema 与隔离 |
| T18 | svcapi：quota-check 三态（有余额/超额/无配额行）；usage 幂等键重复上报不双记；httpapi：quota PUT 下调后 quota-check 立即拒；usage 聚合按 day/model | 控制面契约 |

### 端到端（dev-verify）

| # | 用例 | 目的 |
|---|---|---|
| T19 | `airush-llm` Pod ready；`/health/readiness` 报 `db: Not connected`（确认无状态形态没被误配 DB） | 部署 |
| T20 | port-forward + Secret 里的 master key 打 `chat-default` → 命中 mock sidecar 返回；ConfigMap 渲染物 `grep -c "sk-"` = 0 | 路由通 + 无明文 key |
| T21 | console `/api/v1/llm/quota` 有默认租户 seed 行；PUT 改预算 → GET 回读 | 控制面通 |

> 端到端**用量记账**（agent 调用 → usage 行）要等 1.8 有真实调用方；本 spec 的 T11/T18 已在集成层
> 证明两端各自成立，dev-verify 里"通了"的断言在 1.8 补。

---

## §5 与现有代码的 contract

| 模块 | 动作 | 说明 |
|---|---|---|
| `deploy/charts/airush` | **新增** llm 组件模板 + values | 与 console/gateway 同结构；`_helpers.tpl` 加 master key Secret lookup（复用 connector-pki 的模式）|
| `console/migrations` | **新增** 0005 | 不动 0001-0004 |
| `console/internal/repo` | **新增** `llm.go` | 沿用 `InTenantTx`；不改既有函数 |
| `console/internal/svcapi` | **新增** 两条内部路由 | 认证中间件复用；`ingest.go` 不动 |
| `console/internal/httpapi` | **新增** `llm.go` 三条路由 | 沿用 `pathUUID/parseWindow` 等护栏（`parseWindow` 从 `collected.go` 提到 `respond.go` 共用——纯搬移）|
| `libs/llm` | **新增** | 依赖 `libs/apierror`、`libs/obs`、`console/internal/tenancy`?——**不能**：libs 不得依赖 console。租户从 ctx 取的函数目前在 `console/internal/tenancy`；本 spec 把 `tenancy` 的 ctx 存取部分**提到 `libs/tenancy`**（纯搬移 + console 内保留别名转发），gateway/agent-runtime 都能用。这是本 spec 唯一触碰既有包边界的地方，列入 §7 DoD |
| `agent-runtime` | **不动** | 1.8 再接 |
| `docs/decoupling-architecture.md` | **改**：可替换点表加一行 | "LLM 网关：LiteLLM → Bifrost/自研；替换面 = Helm 组件 + `AIRUSH_AGENT_LLM_URL`；上层零改动" |
| `proto/errors.json` | **追加** 3 码 + `LLM` 域 | 只增不删 |
| `proto/openapi/console.yaml` | **追加** `/api/v1/llm/*` | 契约先行 |

---

## §6 风险

| # | 风险 | 概率 | 缓解 |
|---|---|---|---|
| R1 | **LiteLLM 版本漂移**：迭代极快、偶有破坏性变更（实测镜像 `main-stable` 一周一构建） | 高 | 实施第 1 步改为 **digest 钉版**（1.96.2）；升级走 PR + T11-T16 集成回归；`main-stable` 标签只在探测脚本用 |
| R2 | **Responses→Chat 桥接的语义缺口**（工具调用、多轮 `previous_response_id`、流式事件顺序）——探测只验了最简输入 | 中 | T12 扩到带 tool 定义 + tool_call 回合；1.8 接入时以 codexgo 的真实请求做金丝雀；不过则退 §8 Q3 备选 B（codexgo 对该供应商配 chat 线协议） |
| R3 | **`/metrics` 需 master key**：Prometheus 抓取要带 bearer，本地 otel-lgtm 栈的 scrape 配置要改；或有人图省事把 key 放进 ServiceMonitor 明文 | 中 | 抓取凭据走 Secret 引用（ServiceMonitor `bearerTokenSecret`）；dev 栈只在 dev-verify 里带 key 打一次验可用性，不做长期抓取；Stage 1 主要靠 `airush_llm_*` 平台侧指标 |
| R4 | **Python 进程资源开销**：空载 ~300-400 MB 内存，比 Go 组件重一个量级；多副本时放大 | 中 | requests/limits 基线按实测填；HPA 上限 3；Stage 4 私有化打包若资源紧，走解耦表登记的替换路径 |
| R5 | **配额门 fail-open** 被滥用为"关掉 console 就不限额" | 低 | 放行必计数 + 连续放行 > 20 次告警（1.15 审计事件）；控制面正常时门是硬的；Stage 2 spec-2.8 引入本地令牌桶兜底 |
| R6 | **上游错误正文泄漏**：LiteLLM 错误消息可能带 `api_base`、供应商 request id、甚至回显部分请求 | 中 | Meter 只把状态码与我们的错误码往上传，正文进日志（日志已有脱敏中间件）；T9 固化 |
| R7 | **流式断流丢用量**：客户端取消时末帧 usage 收不到 | 中 | `status=aborted` 记 0 token 但计调用；配额按 token 算会低估——接受，Stage 2 引入按 prompt 长度的保守估算 |
| R8 | **国内供应商 OpenAI 兼容差异**（Qwen/GLM 的兼容模式对 `stream_options`、tool 格式支持不齐）| 中 | 实施第 1 步逐个供应商跑 T11/T12 变体；不支持 `include_usage` 的供应商用 LiteLLM 的 token 估算（它会在缺 usage 时按 tokenizer 估）并标 `cost_ref_micro=NULL` |

---

## §7 DoD

- [ ] D1-D6 全部交付；0005 `up→down→up` 幂等；
- [ ] LiteLLM 镜像以 **digest 钉版**进 values，`main-stable` 标签不出现在 chart 里；
- [ ] Helm 渲染物中无任何供应商 key / master key 明文（T20 断言 + `helm template | grep`）；
- [ ] `libs/llm` 单元 T1-T10 全绿，覆盖率 ≥ 80%；集成 T11-T18 全绿；
- [ ] 三个新错误码入 `proto/errors.json` 且各有触发用例；
- [ ] `libs/tenancy` 提取完成，console 内既有调用零改动（编译期别名），gateway 不受影响；
- [ ] 观测：`airush_llm_*` 三件套接入 spec-0.9；LiteLLM `/metrics` 可用性与鉴权在 T15 固化，抓取方式写进 §2.2 注释；
- [ ] AD-3 证据：T16 canary 用例 + LiteLLM 无 DB/无缓存的配置在 Helm 模板注释里说明"为什么无状态"；
- [ ] dev-verify ALL PASS 含 T19-T21；`make dev-up` 多出的 mock 镜像构建 ≤ 30s；
- [ ] `docs/decoupling-architecture.md` 可替换点表加 LLM 网关一行；
- [ ] OpenAPI 契约同步；`.env.example` 一致性门闩过；
- [ ] 覆盖率合并口径：console ≥ 80%、libs-llm ≥ 80%；CI 全绿；
- [ ] 文档同步：spec 状态、roadmap §8、CHANGELOG；1.8 依赖的接口（`Meter`、`AIRUSH_AGENT_LLM_*`）在本 spec §2.4/§2.6 定版，1.8 起草时不再改。

---

## §8 Q&A（决策点）

### Q1：LiteLLM 跑什么形态？

- **★ A. 无状态纯路由**：无 `DATABASE_URL`、无 Redis、无虚拟 key；只做协议翻译 + 路由 + fallback。
  理由：① AD-3——它不持久化任何东西，prompt 不落它的库；② 配额一份事实源在控制面；③ 可随意扩缩、
  可替换（换 Bifrost/自研只动 Helm）；④ 实测该形态完整可用（含 metrics）。
- B. 全功能（挂 PG + Redis，用它的团队/预算/spend log）。省写配额代码，但配额两份事实源、
  prompt 进它的 spend log、多一套要备份的库、UI/管理 API 攻击面。

### Q2：配额在哪执行？

- **★ A. 平台侧，调用方进程内的 `Meter`（RoundTripper）+ console 内部 API**。理由：与 gateway→console
  同一模式（DB 访问只在 console）；对 codexgo 客户端零侵入；LiteLLM 无感知。
- B. LiteLLM 内置预算（见 Q1-B）。
- C. 独立 sidecar 代理在 LiteLLM 前面做配额。多一跳、多一个组件；且 sidecar 要解 SSE 才能拿 usage——
  和 Meter 做的事一样，只是位置更差。

### Q3：agent 对 chat-only 供应商用什么线协议？

- **★ A. agent 统一说 Responses API，由 LiteLLM 用供应商原生前缀桥接成 chat**。理由：codexgo 主路径
  是 Responses；一套 agent 配置不随供应商分叉；实测桥接可用。**前提**：T12 扩到工具调用与多轮仍过；
  不过则该供应商退到 B。
- B. codexgo 侧对该供应商配 `wire_api=chat`，LiteLLM 只透传。少一层转换，但 agent 配置按供应商分叉，
  且 codex 的 chat 路径维护度低于 Responses 路径。
- 备注：LiteLLM 无原生前缀的供应商（若有）用 `openai/` + `api_base` **只能走 B**（实测 `openai/`
  前缀不桥接）。

### Q4：`llm_usage` 表形态？

- **★ A. 普通 RLS 表 + `(tenant_id, at)` 索引，Stage 1 不分区**；保留策略随 spec-1.15 审计一并定
  （审计元数据与用量同生命周期）。理由：Stage 1 体量小；一次调用一行是审计与精确配额的前提。
- B. 放进 `tsdb`（TimescaleDB 超表 + 压缩 + 保留）。行是"事件"不是"读数"，且需要 AD-10 等效隔离形态——
  为一张年百万行的表引入第二个等效形态使用者不值；spec-1.5 承诺"等效形态唯一使用者"。
- C. 只存日聚合。丢 trace 关联，审计追不到单次调用。

### Q5：配额粒度？

- **★ A. 租户月度 token 预算 + hard_stop 开关**。理由：MVP 单租户，成本护栏够用；token 是所有供应商
  共同的计量单位；金额需定价表且 user 定不做计费。
- B. 按 agent / 按 purpose 细分预算。1.8 有 agent 后再评估（表结构预留 `agent_id/purpose` 列，聚合已可按它们分）。
- C. 按金额。依赖 LiteLLM 定价表准确性，且逼近计费。

### Q6：模型目录放哪？

- **★ A. Helm values → ConfigMap（部署期静态）**。理由：Stage 1 单租户、模型集合是部署决策；
  逻辑名（`chat-default/chat-strong`）是平台侧稳定契约，供应商映射可随时改而不动代码。
- B. 控制面表 + 控制台可配 + LiteLLM 热加载。租户级可见性/偏好归 Stage 2（spec-2.8）。

### Q7：重试 / fallback / 缓存？

- **★ A. 开 fallback（values 可配）+ LiteLLM 默认重试；不开响应缓存**。理由见 §1.2 #3。
- B. 全开含 Redis 缓存。缓存内容含租户数据，隔离与保留要另定；命中率低不值。

---

## §9 实施计划

| # | 步骤 | 估时 |
|---|---|---|
| 1 | D1 Helm（含 mock sidecar）+ 镜像 digest 钉版 + T19/T20；**同时**逐供应商跑探测变体（Responses 桥接 + 工具调用 + `include_usage`），把 R2/R8 的不确定性在第一天消掉 | 1 天 |
| 2 | `libs/tenancy` 提取（纯搬移）+ D2 迁移 + T17 | 0.5 天 |
| 3 | D3 `libs/llm`：TDD，T1-T10 先写 | 1.5 天 |
| 4 | D6 集成 T11-T16（testcontainers 起 LiteLLM + 进程内 mock） | 0.75 天 |
| 5 | D4 控制面 API + T18；D5 观测与错误码 | 1 天 |
| 6 | dev-verify T21、覆盖率、解耦表、OpenAPI、文档、review | 0.5 天 |

总计 **5.25 天**。

> 步骤 1 把供应商兼容性探测放在第一天，理由同 spec-1.5：先证伪最贵的假设——若某供应商的
> Responses 桥接不可用，影响的是 1.8 的 agent 配置形态，越早知道越好。

---

## §10 后续 spec 关联

- **spec-1.8**（Agent Runtime）：codexgo 的 api client 挂 `llm.Meter`；`AIRUSH_AGENT_LLM_URL/KEY`；
  agent 配置里的模型名引用本 spec 的逻辑名；1.8 补 dev-verify 端到端记账断言；
- **spec-1.9**（Skill 框架）：skill 若需调 LLM（不建议——skill 应只处理数据），也经同一网关与 Meter；
- **spec-1.20**（知识库）：embedding 路由复用 LiteLLM `/v1/embeddings` 或独立服务，届时决定；
  `Meter` 对 embeddings usage 的提取分支预留；
- **spec-1.15**（审计）：`llm_usage` 是 LLM 调用审计的数据源（元数据级）；
- **spec-2.8**（速率限制与租户配额）：并发/RPS 限流 + 本地令牌桶兜底 + 按 agent 预算；
- **spec-4.5**（私有化打包）：本地模型经 `hosted_vllm/`、`ollama/` 前缀接入；若替换 LiteLLM，
  按解耦表路径换组件。
