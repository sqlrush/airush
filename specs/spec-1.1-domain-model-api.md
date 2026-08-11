# spec-1.1 控制面领域模型与 API 骨架

> **shipped** — user approve 2026-08-10（Q1-Q6 全采★）；同日实施完成（PR #14）。
> 实施修订：覆盖率口径按 spec-0.4 修订记录 2026-08-10 执行（合并口径，user 选项 1）

## Header / 元数据

- **位置**：Stage 1 第 1 个功能点；前置 Stage 0 全量（spec-0.6 迁移框架/RLS 模板、
  0.7 配置、0.8 错误码、0.9 观测、0.10 部署）；被 spec-1.2（Connector 注册）、
  1.5（采集落库）、1.13/1.14（前端）消费；
- **表结构评审**：六张领域表已于 2026-08-10 user 会话评审冻结（记录于 spec-0.6
  Header）；本 spec §2.1 为其 DDL 定版，**相对评审稿的增量列以 ⊕ 标注**供复核；
- **安全权重**：落地 AD-4②（直连凭据平台侧信封加密）与 AD-10（RLS 的应用层执行
  路径——租户上下文中间件），两者均为核心安全原则；
- **依赖审批**（规则 8）：**零新增后端依赖**——HTTP 用标准库（§8 Q2）、加密用
  标准库 crypto、DB 用已批 pgx/v5（增 pgxpool 子包，同一模块）；
- **决策日期**：2026-08-10，待 user approve。

## §1 范围

### §1.1 包含（Deliverables）

| # | Deliverable | 文件清单 | 估算 | 说明 |
|---|---|---|---|---|
| D1 | 领域表迁移 | `console/migrations/0002_domain_model.up/.down.sql`：users(最小)/connectors/datasource_groups/datasources/datasource_credentials/datasource_aliases/agents 七表，全部租户表套 RLS 模板四要素 + dev 租户 seed | 2 文件 ~260 行 SQL | §2.1 定版；§8 Q1/Q5 |
| D2 | console 服务化骨架 | `console --serve`：net/http server + obs/apierror 中间件链（复用 gateway 模式）、`internal/httpapi/` 路由结构、优雅退出 | ~5 文件 ~300 LOC | §8 Q2 |
| D3 | 租户上下文中间件与 repo 基座 | `internal/tenancy/`（ctx 注入/取用唯一点）+ `internal/repo/`（pgxpool + **事务级 `SET LOCAL app.tenant_id` 强制入口**）；Stage 1 租户来源 = 配置默认租户（认证接管在 spec-2.2） | ~4 文件 ~250 LOC | RLS 的应用层执行路径，AD-10 |
| D4 | 领域 CRUD API | `/api/v1/` datasources（含直连凭据加密写入）、connectors（读）、agents、datasource-groups、aliases；cursor 分页；`Idempotency-Key` 支持（变更类）；OpenAPI 契约 `proto/openapi/console.yaml` | ~10 文件 ~900 LOC + ~350 行 YAML | 契约先行，§8 Q3 |
| D5 | 凭据信封加密 | `internal/credcrypto/`：AES-256-GCM，KEK 经 `AIRUSH_CONSOLE_CREDENTIAL_KEK`（secret，base64 32B），DEK 每凭据随机，`key_id` 承载 KEK 轮换 | ~2 文件 ~150 LOC | AD-4②；§8 Q4 |
| D6 | 测试 | 单测（加密 roundtrip/分页 cursor/校验）+ 集成（**经 API 层的 RLS 隔离断言**、七表迁移 up-down-up、凭据密文落库断言）+ 新错误码触发用例 | ~8 文件 ~700 LOC | 覆盖率阻断开关自本 spec 合并起打开（spec-0.4 约定） |

### §1.2 不包含

| 不做的事 | 理由 |
|---|---|
| 认证/登录/RBAC | spec-2.2 专项；本 spec 租户来源=配置默认租户，中间件接口按终态设计（认证只替换"租户从哪来"一层） |
| 租户管理 API（创建/停用租户） | 多租户运营能力归 spec-2.1；Stage 1 单租户用迁移 seed |
| Connector 注册/心跳协议 | spec-1.2 专项；本 spec 仅 connectors 表读 API（注册协议写入） |
| 采集数据/指标读 API | spec-1.5（TimescaleDB 侧）专项 |
| 凭据连库验证（test connection） | 依赖直连接入器执行面，spec-1.17 承载 |
| skill 注册表 | spec-1.9 随 MCP 框架定（表结构届时按 RLS 模板评审后增） |
| 审计事件写入 | spec-1.15 专项；本 spec 在变更路径预留 hook 点（函数签名）不实现 |

### §1.3 例外说明

users 表提前于认证域建最小占位（§8 Q1）：审计（1.15）与 UI 需要 actor 实体，
仅 id/tenant_id/name/role 四业务列，认证凭据类列一概不建——spec-2.2 增列而非重建。

## §2 接口设计

### §2.1 表 DDL 蓝本（评审稿定版 + 增量标注）

七表全部为租户表（users 亦按租户隔离），统一套 spec-0.6 §2.2 模板四要素
（tenant_id 复合主键 / ENABLE / FORCE / NULLIF policy），下面只列业务列：

- **users**（⊕ 全表为评审后新增，Q1）：`name text NOT NULL`、
  `role text CHECK ('admin','dba','viewer') DEFAULT 'dba'`、时间戳对；
- **connectors**：`name`（租户内 UNIQUE）、`location`、`version`、
  `status CHECK ('online','offline','degraded') DEFAULT 'offline'`、
  `last_heartbeat_at timestamptz`、`cert_fingerprint text`、时间戳对；
- **datasource_groups**：`name`、`kind CHECK ('primary_standby','cluster')`、时间戳对；
- **datasources**：`name`（租户内 UNIQUE）、`engine_family CHECK ('postgres','mysql','dm')`、
  `engine text`、`engine_version text`、`connect_mode CHECK ('connector','direct')`、
  `connector_id uuid NULL`、`credential_id uuid NULL`、`host text`、`port int CHECK (1..65535)`、
  `database_name text`、`group_id uuid NULL` + `group_role CHECK ('primary','standby','replica','node')`、
  `agent_id uuid NULL`、`health_status CHECK ('unknown','ok','warn','crit') DEFAULT 'unknown'`、
  时间戳对；**表级 CHECK×2**：connector 模式⇒connector_id 非空且 credential_id 空，
  direct 模式⇒credential_id 非空且 connector_id 空；⊕ 组内引用一致性
  （group_id 与 group_role 同空同非空）；
- **datasource_credentials**：`username text NOT NULL`、`secret_ciphertext bytea NOT NULL`、
  `key_id text NOT NULL`、`enc_version smallint DEFAULT 1`、`rotated_at timestamptz`、
  ⊕ `created_at`；
- **datasource_aliases**：`datasource_id uuid NOT NULL`、`alias text NOT NULL`、
  `(tenant_id, alias)` UNIQUE、⊕ `source text CHECK ('manual','conversation') DEFAULT 'manual'`
  （助理路由沉淀来源审计）、⊕ `created_at`；
- **agents**：`name`（租户内 UNIQUE）、`kind CHECK ('assistant','domain')`、
  `status CHECK ('running','paused') DEFAULT 'running'`、`instruction_doc text DEFAULT ''`、
  `instruction_version int DEFAULT 1`、时间戳对。

租户内外键统一复合形态 `(tenant_id, x_id) REFERENCES t(tenant_id, id)`（⊕ 明确化：
防跨租户悬挂引用，RLS 之外的第二道结构防线）。seed：dev 租户
`00000000-0000-0000-0000-000000000001`（slug `dev`）+ dev 用户一名。

### §2.2 API 面（OpenAPI 契约先行，摘要）

```
GET/POST      /api/v1/datasources          GET/PATCH/DELETE /api/v1/datasources/{id}
PUT           /api/v1/datasources/{id}/credential      （直连凭据设置/轮换，请求含明文→即刻加密，响应永不回显）
GET           /api/v1/connectors           GET /api/v1/connectors/{id}
GET/POST      /api/v1/agents               GET/PATCH/DELETE /api/v1/agents/{id}
GET/POST      /api/v1/datasource-groups    …/{id}
GET/POST      /api/v1/datasources/{id}/aliases   DELETE …/aliases/{alias}
GET           /healthz
```

- 分页：`?cursor=&limit=`（keyset：created_at+id，cursor 为 base64 不透明串，§8 Q6）；
  响应 `{items, next_cursor}`；
- 变更类接口带 `Idempotency-Key`（表 `idempotency_keys` ⊕，租户表，24h TTL 清理留 1.15 定时任务）；
- 错误响应即 spec-0.8 契约；租户永不出现在路径（standards §5）。

### §2.3 新错误码（注册进 proto/errors.json）

`AR_DATASOURCE_NOT_FOUND`(E3/404)、`AR_DATASOURCE_NAME_CONFLICT`(E3/409)、
`AR_DATASOURCE_MODE_MISMATCH`(E1/400，connect_mode 与 connector/credential 组合非法)、
`AR_DATASOURCE_IN_USE`(E3/409，被 agent/group 引用时删除)、`AR_AGENT_NOT_FOUND`(E3/404)、
`AR_ALIAS_CONFLICT`(E3/409)、`AR_IDEMPOTENCY_REPLAY`(E1/409，同 key 不同 payload)。

### §2.4 凭据加密（AD-4② 定版）

```
KEK：env AIRUSH_CONSOLE_CREDENTIAL_KEK（base64 32B，secret 标签，k8s Secret 注入）
写：DEK=rand32 → AES-256-GCM(DEK, plaintext) → AES-256-GCM(KEK, DEK)
    存 secret_ciphertext = nonce1‖enc(DEK)‖nonce2‖ct，key_id=KEK 版本号
读：仅直连接入器路径（spec-1.17）解密；console API 永不返回明文/密文
轮换：新 KEK 上线→按 key_id 批量重包 DEK（仅重加密 DEK 层，不动数据层）
```

## §3 行为契约

- **一切租户数据访问必须经 repo 基座**（事务内 `SET LOCAL app.tenant_id`）；
  httpapi 包 depguard 禁止直接 import pgx——绕过 repo = 架构违规；
- 租户上下文缺失时 repo 拒绝执行并返回 `AR_TENANT_CONTEXT_MISSING`（fail-closed 双保险：
  即使代码漏注入，RLS 仍兜底 0 行）；
- 凭据明文只存在于"请求解析→加密"的单函数栈帧内，禁日志/禁错误消息（0.7/0.9 双防线之外
  本 spec 加第三道：请求体 dump 类调试代码禁入该路径，review 检查）；
- DELETE 语义：datasources 被引用（agent 绑定/组内）时 409 拒绝，不做级联；
- 兼容性：本 spec 的 API 形态即 `/v1` 契约起点，合并后破坏性变更走 standards §5 弃用流程。

## §4 测试用例

| # | 用例 | 目的 |
|---|---|---|
| T1 | 0002 迁移 up-down-up 幂等，七表+seed 就位 | 迁移健康 |
| T2 | 经 API：租户 A 建 datasource，伪造 B 上下文列表不可见 | RLS 端到端（API→repo→policy） |
| T3 | 未注入租户上下文调 repo → AR_TENANT_CONTEXT_MISSING | fail-closed 应用层 |
| T4 | 直连凭据 PUT → DB 内为密文（无明文子串）→ credcrypto 解密 roundtrip 一致 | AD-4② |
| T5 | API 响应/日志全程无凭据明文（含错误路径） | 泄漏防线 |
| T6 | connect_mode 组合非法（direct+connector_id）→ 400 MODE_MISMATCH | 表级 CHECK 与 API 校验双层 |
| T7 | 每个新错误码 ≥1 触发（规则 4） | 错误码纪律 |
| T8 | keyset 分页：乱序插入 25 条，3 页遍历无重无漏；篡改 cursor → 400 | 分页正确性 |
| T9 | 同 Idempotency-Key 重放：同 payload 返回原结果，不同 payload → 409 | 幂等 |
| T10 | 删除被 agent 绑定的 datasource → 409 IN_USE | 引用保护 |

## §5 与现有代码的 contract

- 修改：console main（--serve 分支）、proto/errors.json（增量域）、.env.example（KEK 等新配置项）、
  values.yaml（console.enabled 默认仍 false，dev 开启走 values-dev + KEK Secret 模板）、
  .golangci.yml（depguard 增 httpapi 禁直连 pgx 规则）；
- 新增：console/internal/{httpapi,tenancy,repo,credcrypto}、0002 迁移、OpenAPI 契约；
- 不动：gateway/skills/前端、libs 三件套 API（纯消费）；
- 覆盖率阻断开关（COVER_ENFORCE）随本 spec 合并在 CI 打开（spec-0.4 既定约定）。

## §6 风险

| 风险 | 概率 | 缓解 |
|---|---|---|
| 某条查询绕过 repo 基座导致 RLS 未设租户（读到 0 行被误判"无数据"） | 中 | depguard 硬禁 httpapi→pgx + repo 为唯一构造入口 + T3 固化；0 行歧义由 fail-closed 语义文档化 |
| KEK 丢失 = 全部直连凭据不可恢复 | 低/致命 | 部署文档红字警示（KEK 必须纳入备份）；key_id 轮换设计使"换钥"不等于"丢钥"；凭据可由用户重录兜底 |
| OpenAPI 契约与实现漂移（手写契约无生成绑定） | 中 | 每接口集成测试断言响应形态；spectral 校验入 lint；Stage 2 评估 oapi-codegen 收紧 |
| 复合外键 + RLS 组合的查询计划劣化 | 低 | 复合索引与主键前缀对齐；Stage 1 验收留 API P99 基线（spec-1.16） |
| Idempotency 表膨胀 | 低 | 24h TTL + 定期清理（1.15 定时任务承载，本 spec 只建表与查询路径） |
| users 最小表与 2.2 认证模型冲突返工 | 低 | 只建四业务列，认证列 2.2 增列；role 枚举与 UI mockup 三角色对齐 |

## §7 DoD

- [ ] D1-D6 就位，T1-T10 全过（集成用例入 CI）
- [ ] 七表 DDL 与 §2.1 蓝本一致（含 ⊕ 增量列），全部租户表模板四要素核对
- [ ] OpenAPI 契约通过 spectral 且每接口有集成测试对照
- [ ] 凭据全链路无明文（T4/T5 证据附 PR）
- [ ] 新错误码注册 + 触发用例 + 生成物同步
- [ ] depguard httpapi 规则生效（注入验证）
- [ ] COVER_ENFORCE 在 CI 打开且本 spec 新包达标（console 模块整体 ≥80%）
- [ ] .env.example/values 同步新配置项
- [ ] dev-up 环境 console.enabled 后 /api/v1/datasources 端到端可用
- [ ] specs 索引与 roadmap 进度表更新；CHANGELOG Unreleased 追加
- [ ] commit 格式合规，独立 PR

## §8 Q&A

**Q1 users 表时机：A. 本 spec 建最小占位（★推荐） B. 推迟 spec-2.2**
推荐 A：审计（1.15）与前端（1.13）都需要 actor 实体，推迟会让两个下游 spec 造临时
概念再返工；最小四列 + 认证列 2.2 增列，无重建风险。

**Q2 HTTP 框架：A. 标准库 net/http + 1.22 增强路由（★推荐） B. chi C. gin**
推荐 A：obs/apierror 中间件链已是 net/http 形态，1.22 ServeMux 支持方法+路径参数已够
CRUD 路由；零新依赖（规则 8 最优）。B 轻但纯增依赖；C 自带生态与我们的中间件/错误
体系重叠冲突。

**Q3 API 契约：A. 手写 OpenAPI 契约先行（★推荐） B. 代码注解生成 OpenAPI**
推荐 A：standards §4 已定"前端类型从 OpenAPI 生成、契约唯一来源"——契约必须先于
实现存在；B 让契约沦为实现的影子，前端被迫跟随实现变更。漂移风险以逐接口集成
测试 + spectral 缓解（§6）。

**Q4 凭据加密：A. 应用层信封加密（AES-GCM + env KEK）（★推荐） B. pgcrypto 库内加密 C. 推迟到 Stage 2 接 KMS**
推荐 A：密文在入库前生成，DB dump/复制流/日志全程无明文，且 KEK 不经过 PG 进程；
B 的密钥随 SQL 语句进入服务端日志面，违背 AD-4 纪律；C 会让 spec-1.17 直连模式
无凭据可用——不可推迟。key_id 字段即未来 KMS 的演进接口。

**Q5 dev 租户 bootstrap：A. 迁移 seed + 配置指定默认租户（★推荐） B. 租户管理 API**
推荐 A：单租户 MVP 无租户管理场景，API 属 spec-2.1 运营域；seed 的固定 UUID 让
本地/CI/kind 三环境一致可断言。

**Q6 分页：A. keyset（created_at,id）+ 不透明 cursor（★推荐） B. offset/limit**
推荐 A：offset 在并发写入下丢行/重行且深页性能差；keyset 是 standards §5
cursor-based 的正解；cursor base64 编码防止调用方依赖内部结构。

## §9 实施计划

| 步骤 | 内容 | 估时（评审轮次口径） |
|---|---|---|
| 1 | D1 迁移 + T1（TDD：先写迁移集成断言） | 1 轮 |
| 2 | D3 tenancy/repo 基座 + T2/T3 + depguard 规则 | 1 轮 |
| 3 | D5 credcrypto + T4/T5 | 1 轮 |
| 4 | D2 服务化 + D4 CRUD 逐资源（datasources 先行）+ T6-T10 | 2 轮 |
| 5 | OpenAPI 契约对照、COVER_ENFORCE 打开、DoD 收尾 | 1 轮 |

## §10 后续 spec 关联

- spec-1.2：Connector 注册协议写 connectors 表（本 spec 的读 API 即其展示面）；
- spec-1.17：直连接入器消费 credcrypto 解密路径（唯一解密方）；
- spec-1.13：前端从 console.yaml 生成 API 类型；
- spec-1.15：审计 hook 点接实现 + idempotency/TTL 清理任务；
- spec-2.1/2.2：租户管理与认证分别接管 bootstrap 与"租户从哪来"。
