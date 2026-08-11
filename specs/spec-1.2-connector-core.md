# spec-1.2 Connector 核心（注册 / mTLS 长连接 / 心跳 / 指令通道）

> **frozen** — user approve 2026-08-11（Q1-Q7 全采★；grpc/protobuf 直接依赖与 buf 工具备案一并批准）
> 本 spec 被指定为**参照模板级**（规则 2）：后续 spec 对照其结构与颗粒度起草。

## Header / 元数据

- **位置**：Stage 1 接入组首件；前置 spec-1.1（connectors 表与只读 API）、spec-0.6（RLS）、
  spec-0.8（错误码）、spec-0.9（观测）；被 spec-1.3/1.4（探针上报）、1.5（数据接入）、
  1.6（脱敏）、1.17（直连接入器复用指令执行框架）、Stage 2 审批执行链消费；
- **上游决策**：AD-2①（outbound-only mTLS 反向隧道）、AD-10（每 Connector 独立 mTLS 证书）、
  AD-9（指令通道帧结构须预留审批令牌位，本 spec 不实现审批）；
- **首个跨服务契约**：本 spec 建立 `proto/` 的 protobuf 契约与代码生成链
  （spec-0.1 预留、spec-0.2 已预写生成代码 lint 豁免）；
- **依赖审批（规则 5 硬门槛 #4，随本 spec approve 一并生效）**：
  - Go 直接依赖新增：`google.golang.org/grpc`、`google.golang.org/protobuf`
    （理由：AD 定版 gRPC/protobuf 契约；两者已是 obs OTLP 链路的间接依赖，升直接引用）；
  - 构建期工具：`buf` CLI 钉版（契约 lint + breaking-change 检测；devDependencies 档，备案制）；
- **决策日期**：2026-08-11，待 user approve。

## §1 范围

### §1.1 包含（Deliverables）

| # | Deliverable | 文件清单 | 估算 | 说明 |
|---|---|---|---|---|
| D1 | proto 契约与生成链 | `proto/connector/v1/{enrollment,session}.proto`、`proto/buf.yaml`、`proto/gen/go/`（独立 Go module）、Makefile `generate-proto`、生成物幂等 CI 守护复用 | ~4 源文件 ~260 行 + 生成物 | §2.2 帧表定版；§8 Q1/Q4/Q5 |
| D2 | console 注册写路径 | `POST /api/v1/connectors`（建实体+签发一次性 enrollment token）、`POST …/{id}/revoke`、内部服务 API `internal/svcapi/`（enroll 校验+签证书、心跳/状态 upsert，服务间 token 认证）；新错误码 AR_CONNECTOR_* 域 | ~6 文件 ~550 LOC | §2.3 流程；§8 Q2/Q3 |
| D3 | 平台内部 CA | `console/internal/pki/`：CA 初始化（chart Secret 承载 CA 键）、CSR 签发（证书 90 天、CN=connector_id、SAN URI 含 tenant_id）、指纹落库、吊销即 DB 状态（会话建立时经 D2 内部 API 校验，不做 CRL 分发） | ~3 文件 ~300 LOC | §8 Q6 |
| D4 | gateway 接入面 | enrollment gRPC（server-TLS + token）、session gRPC（mTLS 双向验证 + 证书↔connector 绑定校验）、会话注册表（内存 map，同 connector 新连踢旧连）、心跳处理→console 内部 API、断线→offline 状态机 | ~7 文件 ~800 LOC | §2.4 状态机 |
| D5 | connector 二进制核心 | `connector/internal/{conf,enroll,session}/`：配置（文件+env）、本地密钥/证书存储（0600，客户侧）、注册客户端、会话循环（指数退避重连、心跳、指令分发器骨架——Stage 1 仅 PING/ECHO 处理器）、`connector --enroll` / `--run` | ~8 文件 ~700 LOC | 探针挂载点留 spec-1.3 |
| D6 | 集成测试与 e2e | 正向：enroll→session→心跳→ECHO→优雅断开；负向：token 过期/复用、吊销证书连接、跨租户 token、证书指纹不匹配、心跳超时降级；dev-verify 增 connector 环节 | ~6 文件 ~900 LOC | §4 |

### §1.2 不包含

| 不做的事 | 理由 |
|---|---|
| 采集数据上报的 payload 定义 | spec-1.3/1.5 专项；session 帧仅留 `DataUpload` oneof 占位（字段编号预留） |
| 受控执行白名单 / 审批令牌验证 | AD-9 归 Stage 2 审批链；`Command` 帧预留 `approval_token` 字段但 Stage 1 仅实现 PING/ECHO |
| 直连接入器形态 | spec-1.17 专项；本 spec 的指令分发器接口按"通道无关"设计供其复用 |
| 脱敏规则引擎 | spec-1.6 专项 |
| Connector 自动升级 | 交付运维专项（Stage 4 私有化前不做）；版本号经 Hello 帧上报供控制台展示 |
| 多网关横向扩展的会话路由 | k8s-scaling-design 已有设计，单网关副本满足 Stage 1 验收；规模化随 Stage 2 |
| 证书自动轮换（到期前静默续签） | 90 天有效期 + 到期前 30 天告警（观测指标）；续签走重新 enroll（复用 D2 全流程），自动化续签列 Stage 2 backlog |

### §1.3 例外说明

无偏离。proto 生成链为 spec-0.1 预留槽位的实装，不构成架构变更。

## §2 接口设计

### §2.1 组件与信任边界

```
客户内网                                   平台 k8s
┌─────────────┐  ①enroll(server-TLS+token) ┌──────────┐ 内部API(svc token) ┌─────────┐
│  connector   │ ─────────────────────────▶│ gateway  │──────────────────▶│ console  │
│  (单二进制)   │  ②session(mTLS 双向)       │ (无状态)  │   校验/签发/心跳     │ (含 PKI) │
└─────────────┘ ◀───── 指令下发 ────────────└──────────┘                   └────┬────┘
      密钥/证书仅存客户侧（0600）                                        控制面 PG（RLS）
```

- gateway 不触碰控制面 schema——一切写经 console 内部服务 API（§8 Q3）；
- enrollment token / 证书 / svc token 三种凭据职责互不交叉。

### §2.2 session 帧（protobuf oneof，定版）

| 方向 | 帧 | 字段要点 | 说明 |
|---|---|---|---|
| C→S | Hello | connector_id、version、started_at | 会话首帧，证书 CN 必须与 connector_id 一致 |
| C→S | Heartbeat | seq、采集器健康摘要（map<string,string> 预留） | 默认 15s 间隔（服务端 HelloAck 可调） |
| C→S | CommandResult | command_id、status、payload、error{code,message} | 与 Command 一一对应 |
| C→S | DataUpload | **占位**（字段编号预留，spec-1.3/1.5 填充） | 本 spec 不实现 |
| S→C | HelloAck | heartbeat_interval、server_time | 会话参数下发 |
| S→C | HeartbeatAck | seq | 缺 3 个 ack 客户端主动重连 |
| S→C | Command | command_id、type、payload、`approval_token`（预留） | Stage 1 type 仅 PING/ECHO |
| S→C | Drain | reason | 网关优雅下线/踢旧连，客户端限期重连 |

### §2.3 注册与凭据流程（定版）

1. 运营者调 `POST /api/v1/connectors {name, location}` → 实体（status=pending）+
   **一次性 enrollment token**（随机 32B，TTL 15 分钟，哈希落库，明文仅响应一次）；
2. 客户侧 `connector --enroll --token … --gateway …`：本地生成密钥对（不出客户侧）→
   CSR 经 gateway `Enroll` RPC（server-TLS）→ console 内部 API 校验 token（一次性、未过期、
   状态 pending）→ PKI 签发 90 天证书（CN=connector_id）→ 指纹落库、status=enrolled、token 作废；
3. `connector --run`：mTLS 建立 session → gateway 校验证书链 + CN↔指纹↔状态（经内部 API）→
   Hello/HelloAck → status=online；
4. 吊销：`POST …/{id}/revoke` → status=revoked → 存量会话 Drain 断开，后续握手拒绝。

### §2.4 connector 状态机（connectors.status 扩展）

```
pending ──enroll──▶ enrolled ──session──▶ online ⇄ degraded（心跳缺口 ≥3 周期）
   │                   │                    │
   └────revoke────────┴──────revoke────────┴──▶ revoked（终态）
                                  online/degraded ──断连/超时 5min──▶ offline ──重连──▶ online
```

迁移 0003：status CHECK 扩展为六值 + `enroll_token_hash`、`enroll_token_expires_at`、
`revoked_at` 列（**表结构实施前与 user 过一遍**，沿用 spec-1.1 评审惯例）。

### §2.5 新错误码（proto/errors.json 增量）

`AR_CONNECTOR_ENROLL_TOKEN_INVALID`(E2/401)、`AR_CONNECTOR_ALREADY_ENROLLED`(E3/409)、
`AR_CONNECTOR_REVOKED`(E2/403)、`AR_CONNECTOR_OFFLINE`(E5/503，指令下发时无会话——
预埋，正式消费在执行链)、`AR_SVC_UNAUTHENTICATED`(E2/401，内部 API service token 无效)。

## §3 行为契约

- Connector 出站唯一：除 gateway 地址外不连任何平台端点；平台侧永不入站连接客户网络；
- 私钥永不离开客户侧；enrollment token 明文仅在创建响应出现一次，日志/审计只见前 8 位；
- 会话唯一：同 connector_id 二连踢一连（旧连收 Drain），杜绝脑裂双活；
- 心跳状态迁移由 gateway 单点判定（console 只记录），阈值可配置且有默认值；
- 内部服务 API 仅集群内可达（NetworkPolicy 列 Stage 2，Stage 1 靠 svc token + 非公开 Service）；
- 生成代码不手改（spec-0.2 豁免路径）；proto 破坏性变更被 buf breaking 在 CI 拦截；
- 兼容性：session 帧新增字段只增不改号；`connector/v1` 包名即版本承诺。

## §4 测试用例

| # | 用例 | 目的 |
|---|---|---|
| T1 | e2e：create→enroll→session→心跳×3→ECHO→优雅断开→offline | 主链路 |
| T2 | token 过期 / 二次使用 / 跨租户 token → 拒绝且码正确 | 注册安全 |
| T3 | 吊销后：存量会话被 Drain、新握手被拒 | 吊销闭环 |
| T4 | 伪造证书（自签同 CN）连接 → TLS 层拒绝 | mTLS 有效性 |
| T5 | 证书合法但指纹与库不符（重 enroll 后旧证书）→ 拒绝 | 指纹绑定 |
| T6 | 断网重连：退避序列正确，重连后 status 回 online | 弹性 |
| T7 | 心跳缺 3 周期 → degraded；超时 → offline；恢复 → online | 状态机 |
| T8 | 同 connector 双连 → 旧连收 Drain | 会话唯一 |
| T9 | 每个新错误码 ≥1 触发用例 | 错误码纪律 |
| T10 | buf breaking：改字段号的 PR 被 CI 拒 | 契约守护 |
| T11 | 内部 API 无/错 svc token → 401 | 服务间认证 |
| T12 | `make generate-proto` 幂等（二跑零 diff） | 生成链健康 |

## §5 与现有代码的 contract

- 修改：console（connectors 写路径 + 0003 迁移 + svcapi）、gateway（由观测演示壳转正式接入面，
  /demo 保留）、connector 模块（占位转实装）、proto/errors.json、.env.example×3、
  Helm（gateway Service 增 gRPC 端口、console CA Secret、svc token Secret）、dev-verify；
- 新增：proto/connector/v1、proto/gen/go module（入 go.work 与 GO_ALL）、connector 内部包；
- 不动：libs 三件套 API、spec-1.1 领域 API 契约（connectors 读 API 形态不变）；
- 对后续：session 帧 oneof 与指令分发器接口是 1.3/1.5/1.17 与 Stage 2 执行链的稳定挂载点。

## §6 风险

| 风险 | 概率 | 缓解 |
|---|---|---|
| 企业网络对 gRPC/h2 长连接不友好（代理/防火墙中断） | 中 | 心跳即保活探测 + 指数退避重连（上限 5min）+ 断连原因入观测；PAC/代理支持预研列 backlog（同步簇 G 参考） |
| 自建 CA 私钥泄漏 = 全部 Connector 身份可伪造 | 低/致命 | CA 键仅 chart Secret（禁 values 明文）+ 指纹二次绑定（伪造证书过不了指纹校验 T5）+ 吊销通道常备；KMS 托管列 Stage 2 |
| gateway↔console 内部 API 成为可用性单点（console 挂则无法建立新会话） | 中 | 存量会话不受影响（校验仅在握手时）；内部 API 加重试 + gateway 侧短期校验缓存（TTL 60s，仅正向结果） |
| 时钟偏移导致证书 NotBefore 校验失败 | 低 | 签发 NotBefore 回拨 5min；HelloAck 带 server_time 供 connector 侧告警 |
| proto 生成链跨 VM/Mac 架构再踩共享缓存坑 | 中 | buf 钉版进 bin/tools 版本化路径（沿 golangci 方案）+ 生成物幂等 CI 守护兜底 |
| 会话注册表在 gateway 重启时全量断连引发重连风暴 | 中 | 客户端退避带 jitter；网关优雅下线先广播 Drain 分批断开 |

## §7 DoD

- [ ] D1-D6 就位，T1-T12 全过（e2e 入 CI 集成 job）
- [ ] 0003 迁移表结构经 user 评审后实施；模板四要素核对
- [ ] proto 生成链幂等 + buf lint/breaking 入 CI
- [ ] 新错误码注册、触发用例、双语言生成物同步
- [ ] enrollment token / 私钥 / CA 键全链路无明文入库入日志（注入式验证记录附 PR）
- [ ] 会话唯一性与状态机全路径实证（T7/T8）
- [ ] dev-verify 增 connector enroll+heartbeat 端到端断言，kind 环境全绿
- [ ] 覆盖率合并口径达标（connector/gateway/console 各 ≥80%）
- [ ] .env.example / Helm values / values-dev 同步，KEK 类 Secret 纪律核对
- [ ] specs 索引与 roadmap 进度表更新；CHANGELOG Unreleased 追加
- [ ] 新依赖（grpc/protobuf/buf）在 PR 描述备案并与本 spec 审批记录互链
- [ ] commit 格式合规，独立 PR 序列

## §8 Q&A

**Q1 隧道传输：A. gRPC bidi stream over mTLS（★推荐） B. WebSocket + protobuf C. 自定义 TCP 帧协议**
推荐 A：与 proto 契约同栈零转换、bidi stream 原生匹配"上行心跳/数据 + 下行指令"、
拦截器与 obs/apierror 链路整合成本最低；B 需自建帧序列化与 keepalive 语义；
C 自造轮子且调试工具链为零。h2 穿透风险列 §6 并有缓解。

**Q2 注册凭据形态：A. 一次性 enrollment token + CSR + 短期证书（★推荐） B. 平台预生成证书人工分发 C. 长期 API key**
推荐 A：私钥不出客户侧（AD-4 精神）、token 短 TTL 且一次性使爆炸半径趋零；
B 私钥经人手流转即违反凭据边界；C 长期静态凭据无法承载 mTLS 双向验证与吊销语义。

**Q3 gateway 写控制面：A. 经 console 内部服务 API（★推荐） B. gateway 直连 PG C. 经消息队列异步**
推荐 A：控制面 schema 与 RLS 纪律收口在 console 单点（spec-1.1 repo 基座直接复用），
gateway 保持无状态纯接入；B 让两个服务耦合同一 schema，迁移与租户纪律双份维护；
C 为低频写引入新基础设施（规则 8 审批 + 运维成本），过度设计。

**Q4 生成代码归属：A. `proto/gen/go` 独立 Go module（★推荐） B. 各消费模块各自生成 C. 放 libs/**
推荐 A：单份生成物多方 import，版本随 proto 源一体演进，幂等守护只盯一处；
B 同一契约多份生成物必然漂移；C libs 是自研库语义，混入生成物污染覆盖率与 lint 口径。

**Q5 契约工具：A. buf 钉版（★推荐） B. 裸 protoc + 插件链**
推荐 A：lint + breaking-change 检测是契约纪律的机器面（T10），一个二进制管完；
B 需手工组装 protoc+插件版本矩阵，且无 breaking 检测——纪律靠 review 人肉，必漏。

**Q6 CA 形态：A. 平台自建内部 CA，console 持有（★推荐） B. cert-manager C. 外部 PKI 对接**
推荐 A：Connector 证书是纯内部身份（唯一消费方是 gateway），自建 CA ~200 LOC 换零新组件；
B 引入集群组件解决单一签发场景，杀鸡用牛刀且本地部署形态多一个交付依赖；
C 客户 PKI 对接是私有化定制需求（spec-4.5 再议）。

**Q7 会话冲突策略：A. 新连踢旧连（★推荐） B. 拒绝新连**
推荐 A：客户侧重装/换机后旧 TCP 常呈半死状态，B 会把用户锁在门外直到超时；
A 配合 Drain 帧让旧端体面退出，T8 固化语义。

## §9 实施计划

| 步骤 | 内容 | 估时（评审轮次口径） |
|---|---|---|
| 1 | D1 proto 契约 + 生成链 + T10/T12（TDD：先立契约守护） | 1 轮 |
| 2 | 0003 迁移（**先过 user 表结构评审**）+ D2 console 写路径/svcapi + T2/T9/T11 | 1-2 轮 |
| 3 | D3 PKI + 签发/吊销 + T4/T5 | 1 轮 |
| 4 | D4 gateway 接入面 + 会话注册表 + T3/T7/T8 | 2 轮 |
| 5 | D5 connector 二进制 + 重连/心跳 + T6 | 1-2 轮 |
| 6 | D6 e2e + dev-verify + 覆盖率 + DoD 收尾 | 1 轮 |

## §10 后续 spec 关联

- spec-1.3/1.4：探针挂进 connector 运行时，DataUpload 帧填充 payload；
- spec-1.5：gateway 数据接入面消费 DataUpload → TimescaleDB；
- spec-1.6：脱敏引擎挂在探针与上报之间；
- spec-1.17：直连接入器复用指令分发器与探针（通道换直连）；
- Stage 2 审批执行链：Command.approval_token 从预留转实装（AD-9）；
- spec-1.13：控制台接入向导消费 create+token 流程与状态机展示。

## 实施修订记录

| 日期 | 修订 | 依据 |
|---|---|---|
| 2026-08-11 | 实施完成。实施期发现与决策（rule 5 范围内，记录于此）：① connector 注册面/会话面在网关是**两个端口**（server-TLS vs mTLS，TLS 配置不可共端口），connector 配置遂拆 `ENROLL_ADDR`/`SESSION_ADDR`；② 集成测试捕获 connector 会话循环并发 `stream.Send` 隐患（gRPC 客户端流非并发安全），改单发送方模型；③ Helm `genCA` 产 RSA/PKCS1 键，`pki.Load` 扩展为兼容 RSA/PKCS8/EC；④ **已知欠账（P2）**：迁移 Helm hook 为 post-install/upgrade，若同批部署的组件（如 console）未就绪导致 `helm upgrade` 失败，post 阶段迁移 hook 不执行——schema 变更被部署健康度间接门控。dev 环境靠健康后重跑 upgrade 落库；Stage 2 评估迁移与部署解耦（独立迁移流水或 pre-hook 化）。登记 roadmap backlog。 | spec-1.2 实施 |
