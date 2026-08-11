# spec-1.3 openGauss（PG 协议族）采集：指标

> **DRAFT — 待 user approve**（Stage 1 严格事前批准：approve 前不编码）

## Header / 元数据

- **位置**：Stage 1 采集组首件（roadmap 序：1.17 → 1.3 → 1.4）；前置 spec-1.17
  （Accessor 通道无关抽象、Direct 连接池）、spec-1.2（DataUpload 帧预留、connector 会话）、
  spec-1.1（datasources 表）；被 spec-1.4（复用探针框架加慢日志/元数据）、spec-1.5
  （数据接入层消费 metric batch → TimescaleDB）、spec-1.10（巡检报告读采集数据）消费；
- **上游决策**：AD-3（Connector 内置固定采集探针上报**结构化**数据，不上报原始数据）、
  AD-7（指标存 TimescaleDB——本 spec 只到 Sink 接口，落库归 spec-1.5）；
- **核心定位**：**一套探针、两通道运行**——探针=对最小 `Querier` 接口执行的指标查询集，
  Direct 通道经 directconn 连接池执行（平台侧），Connector 通道经客户侧 DB 连接执行并走
  DataUpload 帧上报；两通道产出同构 metric batch，兑现 spec-1.17/1.2 §10 的通道无关承诺；
- **蓝本**：openGauss（PG 协议族）；指标查询用 PG 系统视图（pg_stat_*），达梦/MySQL 族
  Stage 3 按引擎方言另立探针实现，本 spec 探针接口保持引擎无关；
- **依赖审批（规则 5 硬门槛 #4）**：无新增第三方依赖——Direct 复用已批 pgx；Connector
  侧 DB 连接同样 pgx（已在 connector 模块依赖树）；
- **决策日期**：2026-08-11，待 user approve。

## §1 范围

### §1.1 包含（Deliverables）

| # | Deliverable | 文件清单 | 估算 | 说明 |
|---|---|---|---|---|
| D1 | 指标模型与目录 | `libs/metrics/`：`Metric{Name,Value,Unit,Labels,At}` + `Batch`；`catalog.go` Stage-1 openGauss/PG 指标目录（连接数/TPS/缓存命中率/复制延迟/锁等待/长事务等，含采集 SQL + 单位 + 版本号） | ~3 文件 ~260 LOC | §2.1；§8 Q1 |
| D2 | 通道无关探针 | `libs/metrics/probe.go`：`Probe.Collect(ctx, Querier) (Batch, error)`——对 `Querier`（`Query(sql)→rows`）执行目录中每条指标 SQL、解析为结构化 Metric；引擎无关、无副作用只读 | ~2 文件 ~220 LOC | §2.2；§8 Q3 |
| D3 | 采集调度器 | `console/internal/collector/`：每 datasource 周期采集（间隔可配、抖动、单实例并发上限），经 Accessor 触发探针 → Sink；采集失败退避不阻断其他实例 | ~4 文件 ~360 LOC | §2.3 |
| D4 | 双通道上报 + Sink | Direct：directconn.Querier 适配 + 直采→Sink；Connector：connector 侧 DB 连接 + 探针执行 + `DataUpload` 帧填充（spec-1.2 保留字段）→ gateway 转 Sink；`Sink` 接口（spec-1.5 落 TimescaleDB，本 spec 提供 buffer 实现 + 计数指标） | ~6 文件 ~500 LOC | §2.4；§8 Q2 |
| D5 | 测试 | 单测（目录 SQL 齐备、探针解析、调度器周期/退避、batch 同构）+ 集成（真 openGauss + 原生 PG 各跑探针，指标值合理域断言；两通道产出同构；Sink 收讫；采集不含原始数据 AD-3） | ~7 文件 ~750 LOC | §4 |

### §1.2 不包含

| 不做的事 | 理由 |
|---|---|
| TimescaleDB 落库 / 时序 schema | spec-1.5 专项；本 spec 到 `Sink` 接口为止，用 buffer 实现验证链路 |
| 慢查询日志 / 表结构 / 实例配置采集 | spec-1.4 专项；本 spec 只做**指标**，spec-1.4 复用探针框架加采集类型 |
| 客户侧脱敏规则引擎 | spec-1.6 专项；本 spec 只上报结构化指标（无原始数据/无 PII），脱敏对指标近乎 no-op |
| 指标告警 / 阈值 / 巡检报告 | 分析类归 skill（spec-1.10+）；采集层只产数据不判断 |
| 达梦 / MySQL 族指标方言 | 蓝本 openGauss（PG 协议族）；探针接口引擎无关，其余引擎 Stage 3 |
| 采集数据的读 API / 可视化 | 读侧归 spec-1.5（存储）+ 1.13（前端）；本 spec 只写不读 |
| 动态指标目录热更新 | Stage 1 目录随版本发布（编译期常量）；运营可配目录列 Stage 2 |

### §1.3 例外说明

无偏离。`libs/metrics` 置于 libs（connector 与 console 双侧共享探针，同 spec-1.17
Accessor 的 depguard 边界考量）。

## §2 接口设计

### §2.1 指标模型（定版）

```go
type Metric struct {
    Name   string            // 目录名，如 "pg.connections.active"
    Value  float64
    Unit   string            // "count"/"ratio"/"ms"/"bytes"
    Labels map[string]string // {datasource_id, database, ...}；禁高基数/禁原始数据
    At     time.Time         // 采集时刻（UTC）
}
type Batch struct {
    DatasourceID string
    EngineFamily string
    Metrics      []Metric
    CollectedAt  time.Time
}
```

- 目录版本号随 batch 上报，供 spec-1.5 schema 演进对齐；Labels 白名单（禁 tenant_id
  等高基数、禁任何行级客户数据——AD-3 只结构化指标）。

### §2.2 探针（通道无关）

```go
type Querier interface {  // Direct=pgxpool、Connector=客户侧 pgx conn 均满足
    QueryMetric(ctx, sql string) ([]map[string]any, error)
}
func (p Probe) Collect(ctx, q Querier) (Batch, error)  // 遍历目录、只读执行、解析
```

- 探针无状态、只读；单条指标 SQL 失败记 partial（该指标缺失）不中断整批；
- 与 spec-1.17 Accessor 衔接：新增 `PROBE_METRICS` 指令类型，Accessor.Dispatch 对该类型
  调用 Probe.Collect（Direct 用连接池、Connector 用本地连接），结果序列化回 Result/DataUpload。

### §2.3 调度（定版）

- 每 datasource 一采集循环：间隔默认 60s（可配 15–3600s）、启动抖动、per-instance 单采集
  在途（防堆积）；采集失败指数退避（上限 5min）不影响其他实例；
- 调度器读 datasources（connect_mode 决定走 Direct/Connector 的 Accessor）；datasource
  删除/停用即停采集循环。

### §2.4 双通道上报

```
Direct:    collector → directconn.Accessor(PROBE_METRICS) → Probe(pool) → Batch → Sink
Connector: collector → gateway 下发 Command(PROBE_METRICS) → connector 侧 Probe(local conn)
           → CommandResult / DataUpload 帧 → gateway → Sink
```

- `Sink interface { Publish(ctx, Batch) error }`；本 spec buffer 实现（内存环形 + 计数
  metric）；spec-1.5 实现 TimescaleDB Sink；
- Connector 侧新增 DB 连接能力（pgx，凭据在客户侧，spec-1.2 凭据边界不变）。

### §2.5 新错误码（proto/errors.json 增量）

`AR_METRICS_COLLECT_FAILED`(E5/502，探针整批失败：连接/查询)、
`AR_METRICS_PARTIAL`(E5/206 语义占位→用 200+partial 标记，不单列 HTTP)——实际以 Batch
内 partial 标记表达，仅 COLLECT_FAILED 入错误码。

## §3 行为契约

- 探针只读：仅 SELECT 系统视图，绝不改库、绝不读业务表行数据（AD-3 结构化指标边界）；
- 上报数据零原始内容：Metric.Labels 白名单校验，出现非白名单键即丢弃并告警（构造期防线）；
- 两通道同构：同一 datasource 经 Direct 或 Connector 采集，Batch.Metrics 的 Name 集合一致
  （值因时刻不同可异），集成测试固化；
- 采集失败隔离：单实例采集异常退避重试，不阻断调度器其他实例、不影响 API 面；
- 凭据边界不变：Direct 凭据平台加密（spec-1.17）、Connector 凭据客户侧（spec-1.2），探针
  代码不接触凭据明文（经各自 Accessor 的连接执行）；
- 兼容性：Metric/Batch/Sink 是 spec-1.4/1.5 的稳定契约，目录只增指标不改已发布指标语义。

## §4 测试用例

| # | 用例 | 目的 |
|---|---|---|
| T1 | 指标目录每条 SQL 对真 openGauss 可执行且返回预期列 | 目录有效性 |
| T2 | 探针对真 PG 采集：连接数/缓存命中率等在合理域（如命中率 0–1） | 值合理性 |
| T3 | 单条指标 SQL 故障 → 该指标缺失、其余照采（partial） | 失败隔离 |
| T4 | Direct 与 Connector 两通道采同一实例 → Metric.Name 集合一致 | 通道无关 |
| T5 | 调度器周期采集：N 个周期产 N 批；实例删除即停 | 调度正确性 |
| T6 | 采集失败 → 退避序列正确、不阻断其他实例 | 弹性隔离 |
| T7 | Metric.Labels 含非白名单键 → 丢弃 + 告警 | AD-3 数据边界 |
| T8 | Sink 收讫计数与产出批数一致；buffer 满环形淘汰 | 上报链路 |
| T9 | 探针执行期无任何 INSERT/UPDATE/DDL（只读断言） | 只读契约 |
| T10 | 每个新错误码 ≥1 触发 | 错误码纪律 |
| T11 | Connector DataUpload 帧携带 batch，gateway 转 Sink 收讫 | Connector 通道端到端 |

## §5 与现有代码的 contract

- 修改：proto/errors.json（增量）、libs/accessor（增 PROBE_METRICS 类型 + Result 承载 batch）、
  console（collector 装配 + directconn Querier 适配 + Sink 装配）、connector（DB 连接 +
  探针执行 + DataUpload 帧填充）、gateway（DataUpload → Sink 转发）、.env.example（采集间隔）、
  dev-verify（Direct 采集一批断言）；
- 新增：libs/metrics、console/internal/collector；
- 不动：spec-1.1 datasources schema、spec-1.17 directconn 连接池 API、spec-1.2 会话/凭据边界；
- 对后续：Batch/Sink 是 spec-1.5 落库入口，探针框架是 spec-1.4 采集类型扩展点。

## §6 风险

| 风险 | 概率 | 缓解 |
|---|---|---|
| openGauss 系统视图与原生 PG 差异（pg_stat_* 字段缺失/改名） | 中 | 目录每条 SQL 标注最低兼容版本；T1 对真 openGauss + 原生 PG 双验；缺失字段降级为该指标 n/a 不崩 |
| 采集频率过高压垮客户 DB / 连接风暴 | 中 | 默认 60s + per-instance 单采集在途 + 复用 spec-1.17 连接池；间隔下限护栏 15s |
| Connector 侧新增 DB 连接引入客户网络内的新故障面 | 中 | 复用 pgx + 超时 + 探针只读；连接失败退避不影响会话心跳；凭据仍客户侧 |
| 指标 Labels 高基数（如把 query 文本当 label）撑爆时序库 | 中/高 | Labels 白名单构造期校验（T7）；query 级明细归 spec-1.4 慢日志、不进指标 Labels |
| 上报数据误含原始客户数据（违反 AD-3） | 低/致命 | 探针只 SELECT 聚合系统视图、无业务表；T9 只读断言 + T7 白名单；review 逐条目录 SQL 核对无行级数据 |
| 两通道探针实现漂移（Direct/Connector 各写一套） | 中 | 探针=共享 libs/metrics 代码，两通道只提供 Querier；T4 同构断言固化 |

## §7 DoD

- [ ] D1-D5 就位，T1-T11 全过（集成入 CI）
- [ ] 指标目录对真 openGauss + 原生 PG 双验（PG 协议族抽象成立）
- [ ] 两通道同构（T4）+ 只读契约（T9）+ 数据边界（T7）实证
- [ ] 探针为共享代码，Direct/Connector 仅提供 Querier（无实现漂移）
- [ ] Sink 接口 + buffer 实现；Connector DataUpload 端到端（T11）
- [ ] 新错误码注册 + 触发用例 + 生成物同步
- [ ] 覆盖率合并口径达标（libs/metrics、console、connector、gateway ≥80%）
- [ ] .env.example / dev-verify 同步（Direct 采集一批断言）
- [ ] specs 索引与 roadmap 进度表更新；CHANGELOG Unreleased 追加
- [ ] 采集间隔/白名单为受控口径，变更须修订本 spec
- [ ] commit 格式合规，独立 PR

## §8 Q&A

**Q1 指标目录组织：A. 编译期常量目录（SQL+单位+版本，libs/metrics）（★推荐） B. 配置文件外置 C. DB 表存目录**
推荐 A：Stage 1 指标集稳定、随版本发布可审计可 diff；外置配置/DB 表引入热更新与一致性
复杂度而 Stage 1 无需求；运营可配目录（增删指标）列 Stage 2。目录是受控口径（§7）。

**Q2 上报落点：A. Sink 接口 + buffer 实现，TimescaleDB 归 spec-1.5（★推荐） B. 本 spec 直接落 TimescaleDB**
推荐 A：采集与存储分层，本 spec 可独立测试（buffer）不阻塞于 1.5；Sink 契约让 1.5 只换
实现。B 把两个 spec 的复杂度耦合，且 TimescaleDB schema 是 1.5 的定版职责。

**Q3 探针抽象：A. 对最小 Querier 接口的共享探针（★推荐） B. Direct/Connector 各写采集**
推荐 A：兑现"一套探针两通道"（roadmap 核心）——探针是纯查询+解析逻辑，两通道只提供
连接（Querier）；B 必然漂移、维护翻倍、破坏通道无关承诺（T4 会挂）。

**Q4 采集触发：A. console 侧调度器统一驱动（★推荐） B. Connector 自驱周期上报 C. 拉取式（平台按需拉）**
推荐 A：调度集中在 console 便于统一间隔/退避/停采管理、与 datasource 生命周期对齐；
B 让采集策略散在各客户端难统一治理；C 实时拉取不适合周期指标且增平台主动连接面。
Connector 通道下 console 经会话下发采集指令，Connector 执行——驱动集中、执行就近。

**Q5 采集间隔与并发：A. 默认 60s + per-instance 单采集在途 + 下限 15s（★推荐） B. 固定高频 C. 无并发控制**
推荐 A：60s 平衡时效与负载；单采集在途防慢实例堆积；下限护栏防误配高频压垮 DB。
B 一刀切不适配实例差异；C 慢实例会累积采集协程耗尽资源。

## §9 实施计划

| 步骤 | 内容 | 估时（评审轮次口径） |
|---|---|---|
| 1 | D1 指标模型 + 目录 + T1（先立目录，对真 openGauss 验 SQL） | 1-2 轮 |
| 2 | D2 探针 + Querier + T2/T3/T9 | 1 轮 |
| 3 | D4 Direct 通道（directconn Querier + Sink buffer）+ T8 | 1 轮 |
| 4 | D4 Connector 通道（DB 连接 + DataUpload + gateway 转发）+ T11 | 2 轮 |
| 5 | D3 调度器 + T5/T6 + 白名单 T7 + 同构 T4 + 集成/dev-verify/DoD | 1-2 轮 |

## §10 后续 spec 关联

- spec-1.4：复用探针框架，加慢日志/表结构/实例配置采集类型（新 Querier 用法）；
- spec-1.5：实现 TimescaleDB Sink，定版时序 schema，消费 Batch；
- spec-1.6：脱敏引擎挂在 Connector 探针与上报之间（指标近 no-op，慢日志 spec-1.4 才吃重）；
- spec-1.10+：巡检/分析 skill 读采集数据产报告；
- spec-1.16：Stage 1 验收的采集上报性能基线在此 spec 留埋点。
