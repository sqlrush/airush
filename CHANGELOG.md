# Changelog

格式遵循 [Keep a Changelog](https://keepachangelog.com/zh-CN/1.1.0/)；
版本策略见 specs/spec-0.11-versioning-release.md §2.1（平台统一版本，Stage 0 完成 = v0.1.0）。
每个改变行为的 PR 在 Unreleased 段追加条目（PR 模板检查项）；本文件是 release notes 唯一来源。

## [Unreleased]

### Added

- LLM 网关（spec-1.7，AD-8）：智能体框架组第一件——
  - **LiteLLM 无状态纯路由**（digest 钉版 1.96.2；无 DB、无缓存、无虚拟 key）：Helm 组件
    `llm`（ConfigMap 只放 `os.environ/` 引用，master key 与供应商 key 全部 Secret 注入；管理 UI
    关闭、遥测关闭、json 日志、prometheus 回调；startupProbe + 实测内存基线 ~1.05 GiB）；
    dev 形态带 mockllm sidecar 假供应商，dev-verify 不持有真实 key；
  - **配额与用量在平台侧**：`libs/llm.Meter`（`http.RoundTripper`，对 codexgo 客户端零侵入）
    注入租户/agent/会话/trace 头、流式自动补 `include_usage`、chat/responses × 流式/非流式
    四种 usage 提取、调用前配额门（超额拒 / 控制面不可达 fail-open 计数）、调用后记账
    （断流 aborted 记 0 token）、错误映射（上游正文只进日志不透传）；
  - 迁移 0005：`llm_quotas`（租户月度 token 预算 + hard_stop）、`llm_usage`（一次调用一行，
    幂等键，应用角色只增不改）；console 内部 API quota-check/usage + 公开 API
    `/api/v1/llm/{quota,usage}`；`airush_llm_*` 观测三件套；
  - **实测结论进用例**：Responses API → chat-only 供应商仅在用**原生前缀**（`deepseek/`）时
    被桥接（含工具调用），`openai/` 前缀原样透传 404；LiteLLM `/metrics` 须带 master key；
    成功路径 json 日志不含 prompt（AD-3）；
  - `libs/tenancy` 自 console 提取（libs 不得依赖 console），console 保留别名转发；
  - 新错误码 AR_UPSTREAM_LLM_MODEL_UNKNOWN（AR_UPSTREAM_LLM_FAILED/TIMEOUT、AR_QUOTA_EXCEEDED
    复用 spec-0.8 既有）。
- 时序存储（spec-1.5，AD-7/AD-10）：采集数据从内存 buffer 落到 TimescaleDB，**表数固定为 3 张**——
  - `tsdb.series` 通用读数流水（超表，`(租户, 数据源, series, 实体, 值, 时刻)`）：指标、慢查询
    及后续任何"实体 + 数字随时间变化"的产物共用一张表；列存压缩 7 天、保留 14 天；
    两层连续聚合 5m（保留 90 天）/1h（保留 400 天），读路径按**查询窗口起点**自动选层；
  - `collected.entities` 实体字典（省 40% 存储，并给"这条慢 SQL 什么时候第一次出现"稳定挂载点）；
  - `collected.snapshots` 慢变状态（表结构/配置）：**内容哈希去重**，只在变化时新增版本，
    于是这张表天然是变更历史而非每小时一份的重复堆积；
  - **AD-10 等效隔离形态**（列存压缩与 RLS 在同一张表互斥，实测报 `columnstore cannot be used
    on table with row security`）：基表零授权 + `security_barrier` 视图 + `check_option = cascaded`，
    四项门槛各有集成用例固化（压缩下读隔离 / 无上下文 fail-closed / 绕过基表被拒 / 越权写被拒）；
    其中"越权写被拒"在初验时**没拦住**（`security_barrier` 只管读不管写），补 `check_option` 才堵上；
  - 两层指标命名 `db.*`（规范层，跨引擎语义 + 单位统一）+ `pg.*`/`mysql.*`（引擎特有层），
    引擎差异在采集侧消化；Stage 3 接 MySQL/达梦只加编译期目录常量，不加表、不改 schema；
  - 写入经隔离视图（R1 基准实测退化 5.9%，门槛 30%）；gateway 仍**不持有 DB 连接**，
    Connector 上报经 console 内部 API 落库（爆炸半径不扩大）；
  - console 新增采集数据查询面（series 区间 / 快照当前版本与版本链；Top N 排名端点显式 501，留给 spec-1.11——累计计数器需查询侧差分，属慢查询分析 skill），
    带窗口与点数护栏；新错误码 AR_TIMESERIES_WRITE_FAILED/QUERY_FAILED/UNDECLARED_SERIES。
- 慢日志与元数据采集（spec-1.4，AD-3/AD-9）：复用探针框架的第二类采集——**快照**（行结构数据）：
  - libs/metrics：`Snapshot` 信封 + SlowQueryEntry/TableInfo/ConfigEntry 强类型条目 + 三类
    编译期目录（慢查询源候选链 pg_stat_statements → openGauss dbe_perf、表结构 pg_catalog、
    配置 pg_settings 全量）+ `SnapshotProbe`（对新增 `RowQuerier` 执行，与指标探针并列不改其契约）
    + `SnapshotSink`；能力缺失（如未装 pg_stat_statements）走 CapabilityMissing 结构化降级而非报错；
  - 慢查询文本只取统计视图的**规范化**语句（字面量已占位），显式禁 pg_stat_activity.query——
    spec-1.6 脱敏引擎落地前的 AD-3 编译期防线，真 openGauss 上以字面量金丝雀实证；
  - 尺寸有界：TopN 50/表 500/文本 2048 字符/快照 512KB，超限截断并标记；
  - Direct 通道：directconn.SnapshotQuerier → 本进程 SnapshotSink；Connector 通道：
    PROBE_SLOWLOG/PROBE_SCHEMA/PROBE_CONFIG 三指令（每 kind 一类型即白名单，payload 零 SQL）
    → DataUpload(kind) → gateway 分流落 SnapshotSink；collect API 增 kind（缺省 metrics 向后兼容），
    未知 kind 在平台与连接器双侧拒绝；
  - 调度器改为每 datasource×kind 一循环，分 kind 间隔（指标 60s/慢查询 300s/元数据 3600s，各带下限护栏）；
  - 新错误码 AR_SNAPSHOT_COLLECT_FAILED/AR_COLLECT_UNSUPPORTED_KIND。
- 指标采集（spec-1.3，AD-3/AD-7）：一套探针两通道运行——
  - libs/metrics：引擎无关探针（对最小 `Querier` 执行目录 SQL）+ Stage-1 openGauss/PG
    指标目录（连接数/TPS/缓存命中率/复制延迟/锁等待/长事务/库大小，聚合系统视图零行级数据）
    + `Batch`（含 catalog_version/partial/missing）+ label 白名单（防高基数/防原始数据）+ `Sink`
    接口与内存 buffer 实现（spec-1.5 换 TimescaleDB）；
  - Direct 通道：directconn.Querier 适配 + console 采集调度器（每 datasource 周期循环、
    确定性抖动、失败指数退避不阻断其他实例、datasource 增删即起停）→ 本进程 Sink；
  - Connector 通道：连接器侧 dbprobe（客户网络内直连客户库，凭据客户侧 AD-4 不变）执行探针，
    经 DataUpload 帧（proto ClientFrame 字段 4，spec-1.2 预留位实装）回传结构化 batch；
    gateway 会话下发 PROBE_METRICS 指令 + 内部 collect API（svc-token）触发、收 DataUpload
    落 gateway Sink（"gateway 转 Sink"），采集失败经 CommandResult 回错误码；
  - 新错误码 AR_METRICS_COLLECT_FAILED（E5/502）；采集器接入 console/gateway 运行时装配。

- 直连接入模式（spec-1.17，AD-2②）：
  - libs/accessor 通道无关 Accessor 抽象（Connector/Direct 双实现共享 BuiltinDispatch，只读护栏）；
  - console/internal/directconn：从 credcrypto 解密凭据 → 每 datasource pgx 连接池
    （懒建/空闲 TTL 回收/删除销毁），密码经 pgx config 字段注入不进 DSN 字符串（AD-4 第三道防线）；
  - test-connection API（仅 direct 模式、只读、不落库）+ 错误码 AR_DATASOURCE_CONNECT_FAILED/TEST_TIMEOUT；
  - DirectAccessor 实现 accessor.Accessor（编译期断言，与 Connector 通道语义一致）。

### Fixed

- 采集调度器把**无租户上下文**的 ctx 传给落点（spec-1.5 发现）：内存 Sink 不看租户所以一路绿灯，
  换成落库 Sink 后整条采集链 fail-closed 成 `AR_TENANT_CONTEXT_MISSING`。已修并在集成用例中
  加断言（每次落点调用必须携带租户上下文）。
- 本地 kind 栈两处工程缺陷：① console 无 `wait-pg` initContainer，全新集群上 CoreDNS 晚就绪导致
  连续退出，CrashLoopBackOff 退避把就绪推过 helm `--wait` 超时，post-install 的迁移 hook 因而
  从未执行；② `make dev-up` 的 helm 未绑 `--kube-context`，集群已存在时会打到 kubectl 当前 context
  （机器上有其他集群时属于往错误集群发布）。

### Added

- Connector 核心：注册 / mTLS 长连接 / 心跳 / 指令通道（spec-1.2）：
  - proto 契约与 buf 生成链（connector/v1 enrollment+session，幂等守护 + breaking 检测入 CI）；
  - 迁移 0003：connectors 六态状态机 + 一次性注册令牌哈希列 + 吊销时间戳；
  - 注册流程：一次性 token（15min TTL，仅哈希落库）→ CSR → 平台内部 CA 签发 90 天
    客户端证书（CN=connector_id，SAN 绑租户）→ 指纹落库；私钥永不出客户侧；
  - gateway 接入面：enrollment（server-TLS）+ session（mTLS 双向）两个 gRPC 端口、
    会话注册表（新连踢旧连）、心跳状态机（online/degraded/offline）、优雅 Drain；
  - connector 二进制：--enroll / --run，本地 0600 凭据存储、指数退避重连、PING/ECHO 处理器；
  - console 内部服务 API（svcapi，service token 认证）承载注册校验/签发/状态记录，
    gateway 不触碰控制面 schema；
  - 新错误码 5 个（AR_CONNECTOR_*/AR_SVC_UNAUTHENTICATED）；
  - Helm：连接器 PKI Secret（内部 CA + gateway 服务端证书，lookup 复用避免重生成）、
    gateway 接入端口、console CA/svc-token 接线；dev-verify 增 connector enroll→online e2e。

### Fixed

- connector 会话循环并发 stream.Send 隐患：改单发送方模型（集成测试捕获，spec-1.2）；
- pki.Load 兼容 RSA/PKCS8 私钥（Helm genCA 产物），修复 dev 部署 console CA 装载失败。

- 控制面领域模型与 API 骨架（spec-1.1）：
  - 迁移 0002：users/connectors/datasource_groups/agents/datasource_credentials/
    datasources/datasource_aliases/idempotency_keys 八表，全部租户表套 RLS 模板四要素，
    租户内外键复合形态防跨租户悬挂引用；dev 租户 + dev 管理员 seed；
  - `console --serve`：控制面 REST API（datasources/agents/datasource-groups/aliases/
    connectors CRUD），OpenAPI 契约先行（proto/openapi/console.yaml）；
  - 租户上下文基座：tenancy ctx 唯一注入/取用点 + repo 租户事务
    （SET LOCAL ROLE airush_app + app.tenant_id，RLS 应用层执行路径）+
    depguard 硬禁 httpapi 直连 pgx；
  - 直连凭据信封加密（AD-4②）：AES-256-GCM 双层（KEK env/k8s Secret 注入、
    DEK 每凭据随机、key_id 轮换位）；API/日志/响应零回显；
  - keyset 分页（不透明游标）+ Idempotency-Key 幂等（响应快照同事务落库）；
  - 新错误码 7 个（AR_DATASOURCE_*/AR_AGENT_NOT_FOUND/AR_ALIAS_CONFLICT/
    AR_IDEMPOTENCY_REPLAY）；
  - Helm console 组件（Deployment/Service/KEK Secret，dev 默认开启）+
    dev-verify console API 端到端断言。

## [0.1.0] - 2026-08-10

### Added

- monorepo 脚手架与三语言构建体系：Go workspace 四组件、Python uv workspace、前端 Vite（spec-0.1）
- 三语言 lint 体系：golangci-lint v2（depguard 解耦边界）、ruff+mypy strict、eslint strict+prettier（spec-0.2）
- CI/CD：lint/test/build/integration 四 required checks + gitleaks 阻断 + 每日依赖扫描 + 分支保护（spec-0.3）
- 单元测试框架与分层覆盖率闸门（COVER_ENFORCE 开关，报告先行）+ -race 常开（spec-0.4）
- 集成测试框架：testkit 容器封装（PG/Redis）+ schema 每用例隔离 + ci/integration（spec-0.5）
- 控制面迁移框架：console migrate 子命令 + 0001 RLS 基建（airush_app 角色、tenants 表、
  租户表模板四要素含连接池 GUC 空串加固）+ 编号/不可变 CI 守护（spec-0.6）
- 配置框架：libs/config 声明式加载（聚合校验/secret 脱敏/COMMON 回退）+ pydantic-settings 基类 +
  .env.example 一致性守护（spec-0.7）
- 错误码体系：proto/errors.json SSOT（15 码六级分级）双语言生成 + libs/apierror HTTP 中间件
  （panic 恢复/细节不泄漏）+ 码不可删守护（spec-0.8）
- 可观测性基线：libs/obs 三件套（必带字段/双出口 record 级脱敏/OTLP/label 白名单/降级）+
  gateway healthz/demo 端点 + otel-lgtm 本地栈 + 三信号冒烟脚本（spec-0.9）
- 镜像与部署：三类参数化 Dockerfile（distroless/nonroot/只读 rootfs）+ Helm chart
  （gateway/内置存储/migrate hook）+ make dev-up 一键 kind 全栈（spec-0.10）
- 版本与 release 链路：CHANGELOG 机制、release workflow、release-prep 脚本（spec-0.11）
