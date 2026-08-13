# spec-1.4 openGauss（PG 协议族）采集：慢日志与元数据

> **frozen** — user approve 2026-08-12（Q1-Q6 全采★；无新依赖，复用 pgx；无 proto 变更）

## Header / 元数据

- **位置**：Stage 1 采集组次件（roadmap 序：1.3 → 1.4）；前置 spec-1.3（探针框架/Sink/
  DataUpload 通道/collect API）、spec-1.17（directconn 连接池）、spec-1.2（会话指令通道）、
  spec-1.1（datasources 表）；被 spec-1.5（快照落库）、spec-1.6（脱敏引擎——慢日志文本是
  主要吃重对象）、spec-1.10（巡检报告读 schema/config）、spec-1.11（慢查询分析消费
  SlowQueryEntry）、spec-1.16（openGauss 真机验收）消费；
- **上游决策**：AD-3（只上报结构化数据——慢查询文本是本 spec 最敏感载荷，定编译期最小
  防线）、AD-9（指令白名单——采集指令**零 SQL 下发**，SQL 全部来自编译期目录）、
  AD-7（落库归 spec-1.5，本 spec 到 SnapshotSink 为止）；
- **核心定位**：复用 spec-1.3"一套探针两通道"框架，新增三类**快照采集**——慢查询统计
  （slowlog）、表结构（schema）、实例配置（config）。与指标（单值时序）不同，快照是
  **行结构数据**，故新增 `RowQuerier` 最小接口与 `Snapshot` 强类型模型；探针仍引擎无关、
  编译期目录、无状态只读；两通道（Direct/Connector）产出同构快照；
- **蓝本**：openGauss（PG 协议族）。慢查询源做**能力探测**：原生 PG=pg_stat_statements、
  openGauss=dbe_perf 视图族；全不可用降级为结构化 CapabilityMissing 快照（非错误）；
- **依赖审批（规则 5 硬门槛 #4）**：无新增第三方依赖——复用 pgx，解析纯标准库；
  **无 proto 变更**——DataUpload.kind 本为 string（spec-1.3 预留扩展语义），天然承载新 kind；
- **决策日期**：2026-08-12，待 user approve。

## §1 范围

### §1.1 包含（Deliverables）

| # | Deliverable | 文件清单 | 估算 | 说明 |
|---|---|---|---|---|
| D1 | 快照模型与目录 | `libs/metrics/snapshot.go`（Snapshot 信封 + SlowQueryEntry/TableInfo/ColumnInfo/IndexInfo/ConfigEntry 强类型）+ `snapshot_catalog.go`（slowlog 源候选链含能力探测 SQL、schema/config 目录 SQL、上限常量 TopN/MaxTables/文本长度） | ~3 文件 ~380 LOC | §2.1/§2.2；§8 Q1/Q2/Q6 |
| D2 | 快照探针 | `libs/metrics/snapshot_probe.go`：`RowQuerier`（行查询最小接口，既有 Querier 不动）+ `SnapshotProbe.Collect(ctx, q, kind)`——能力探测→源选择→只读执行→强类型解析→截断/防线；未知 kind 白名单拒绝 | ~2 文件 ~300 LOC | §2.3；§8 Q1 |
| D3 | 调度器扩展 | `console/internal/collector/`：target 增 kind 维度（每 datasource×kind 一循环）、分 kind 间隔配置（slowlog 300s / meta 3600s + 下限护栏）、复用 spec-1.3 退避/单在途/生命周期 | ~3 文件 ~240 LOC | §2.4；§8 Q4 |
| D4 | 双通道传输 | directconn `RowQuerier` 适配；`connector/internal/dbprobe` 增 `PROBE_SLOWLOG`/`PROBE_SCHEMA`/`PROBE_CONFIG` 处理 → DataUpload kind=slowlog/schema/config；gateway collect API 请求体增 kind + `SnapshotSink` 收快照；console GatewayClient 带 kind | ~6 文件 ~420 LOC | §2.4/§2.5；§8 Q3 |
| D5 | 测试与环境 | 单测 + 集成（启用 pg_stat_statements 的真 PG 成功路径、缺扩展降级、schema/config 快照、两通道同构、只读断言、字面量注入防线）；dev/CI PG 启用 pg_stat_statements；dev-verify 快照断言 | ~9 文件 ~900 LOC | §4 |

### §1.2 不包含

| 不做的事 | 理由 |
|---|---|
| TimescaleDB / 关系表落库 | spec-1.5 专项；本 spec 到 SnapshotSink 接口为止，buffer 实现验证链路（同 spec-1.3 口径） |
| 慢查询聚类 / 执行计划解读 / 优化建议 | spec-1.11 skill 专项；采集层只产数据不分析 |
| 可配置脱敏规则引擎 | spec-1.6 专项；本 spec 内置防线（规范化文本源 + 截断）是**编译期硬防线**，非规则引擎 |
| 实时活动语句采样（pg_stat_activity.query 文本） | 带字面量的原始 SQL，AD-3 高危载荷；且为瞬时态，与周期快照语义不符——显式禁止（§3） |
| 慢日志文件解析（log_min_duration_statement 日志 tail） | 需文件系统访问面，改变 Connector 部署形态假设；统计视图源已覆盖 Stage 1 分析需求 |
| 达梦 / MySQL 族快照方言 | 蓝本 openGauss（PG 协议族）；目录接口引擎无关，其余引擎 Stage 3 |
| schema 漂移 diff / 变更告警 | 消费侧职责（spec-1.5 存储比对 / spec-1.10+ skill）；采集层只产快照 |

### §1.3 例外说明

无偏离。快照类型并入 `libs/metrics`（复用双通道传输/Sink/测试基建，见 §8 Q5——包名承载
"采集"域的沿革在包 doc 注明）。

## §2 接口设计

### §2.1 快照模型（定版）

```go
type Snapshot struct {
    DatasourceID   string    `json:"datasource_id"`
    EngineFamily   string    `json:"engine_family"`
    Kind           string    `json:"kind"` // slowlog | schema | config
    CatalogVersion int       `json:"catalog_version"`
    CollectedAt    time.Time `json:"collected_at"`
    // CapabilityMissing：数据源缺能力（如未装 pg_stat_statements）——结构化降级，非错误
    CapabilityMissing bool `json:"capability_missing,omitempty"`
    Truncated         bool `json:"truncated,omitempty"` // 任一上限截断则置位
    SlowQueries []SlowQueryEntry `json:"slow_queries,omitempty"` // kind=slowlog
    Tables      []TableInfo      `json:"tables,omitempty"`       // kind=schema
    Configs     []ConfigEntry    `json:"configs,omitempty"`      // kind=config
}
type SlowQueryEntry struct {
    QueryID   string  // 规范化语句标识（queryid / unique_sql_id）
    Text      string  // 规范化文本（字面量已为 $N/? 占位），≤ QueryTextMaxLen
    Truncated bool
    Calls     int64
    TotalMs   float64
    MeanMs    float64
    MaxMs     float64
    Rows      int64
    Database  string
}
type TableInfo struct {
    Schema      string
    Name        string
    Columns     []ColumnInfo // {Name, DataType, Nullable}
    Indexes     []IndexInfo  // {Name, IsUnique, Columns}
    SizeBytes   int64
    RowEstimate int64
}
type ConfigEntry struct{ Name, Value, Unit, Source string }
```

- **上限常量（受控口径，变更须修订本 spec）**：`SlowlogTopN=50`（按累计耗时降序）、
  `SchemaMaxTables=500`（按表大小降序）、`QueryTextMaxLen=2048` 字符、
  `SnapshotMaxBytes=512KB`（序列化后上限，远低于会话帧限）；超限截断并置 Truncated；
- 快照含 CatalogVersion（与 spec-1.3 同一版本号序列），供 spec-1.5 schema 演进对齐。

### §2.2 目录与能力探测

- **slowlog 源候选链**（engineFamily=postgres，按序探测取首个可用）：
  1. `pg_stat_statements`（探测：`pg_extension` 中存在且视图可读）——原生 PG；
  2. `dbe_perf.summary_statement`（探测：命名空间存在且可读）——openGauss，条目标注
     最低兼容版本；
  3. 全不可用 → `Snapshot{CapabilityMissing: true}`（成功路径，供 UI/skill 提示开启）；
- **schema 目录**：pg_catalog（pg_class/pg_attribute/pg_index + pg_total_relation_size +
  reltuples），排除系统 schema（pg_catalog/information_schema/pg_toast*）；
- **config 目录**：pg_settings 全量快照（name/setting/unit/source，§8 Q6）；
- 目录为编译期常量（同 spec-1.3 §8 Q1 决策），每条 SQL 只读、内嵌 LIMIT 上限。

### §2.3 快照探针（通道无关）

```go
type RowQuerier interface { // 既有 Querier（指标单值）不动；两通道适配器同时实现两接口
    // QueryRows 只读执行目录 SQL，返回行（列名→字符串值）；maxRows 为防御性二道上限。
    QueryRows(ctx context.Context, sql string, maxRows int) ([]map[string]string, error)
}
func (p SnapshotProbe) Collect(ctx context.Context, q RowQuerier, kind string) (Snapshot, error)
```

- 探针无状态、只读；解析为强类型条目，逐字段截断（文本超长截断置条目 Truncated）；
- 未知 kind → 错误（映射 `AR_COLLECT_UNSUPPORTED_KIND`，白名单拒绝面之一）；
- 能力探测失败（源候选全不可用）走 CapabilityMissing 降级，**不**进入退避风暴。

### §2.4 调度与双通道

```
Direct:    collector(kind 循环) → directconn RowQuerier → SnapshotProbe → SnapshotSink(console buffer)
Connector: collector → gateway POST /internal/v1/collect{kind} → Command(PROBE_SLOWLOG|PROBE_SCHEMA|PROBE_CONFIG)
           → connector dbprobe(本地连接) → DataUpload(kind=slowlog|schema|config) → gateway SnapshotSink
```

- 每 datasource×kind 一采集循环；默认间隔：metrics 60s（既有）、slowlog 300s、
  schema/config 3600s；可配 + 下限护栏（slowlog ≥60s、schema/config ≥300s）；
  复用 spec-1.3 退避（上限 5min）/单在途/删除即停；
- `SnapshotSink interface { PublishSnapshot(ctx, Snapshot) error }`——与既有 Sink 并列的
  小接口（接口隔离），BufferSink 同时实现；spec-1.5 统一落库；
- collect API 请求体增 `kind` 字段（缺省 "metrics"，对 spec-1.3 调用方向后兼容）；
  成功仍只回触发终态，数据走 DataUpload→Sink 不经此回流（spec-1.3 §11 定案沿用）。

### §2.5 新错误码（proto/errors.json 增量）

- `AR_SNAPSHOT_COLLECT_FAILED`（E5/502，快照整批失败：连接/查询/权限）；
- `AR_COLLECT_UNSUPPORTED_KIND`（E4/400，采集类型不在白名单——AD-9 显式拒绝面）。

## §3 行为契约

- **指令零 SQL（AD-9）**：平台→连接器指令 payload 仅 `{kind, datasource_id, engine_family}`；
  SQL 全部来自编译期目录；未知 kind 双侧拒绝（console 入口校验 + connector 白名单）；
- **慢查询文本最小防线（AD-3，spec-1.6 之前的编译期硬防线）**：只采规范化统计视图文本
  （字面量已为占位符）；**禁止**从 pg_stat_activity.query 取带字面量原始 SQL；文本截断
  QueryTextMaxLen；集成测试以已知字面量注入断言不出现在上报（T3）；
- 快照只读：仅 SELECT 系统视图/目录，绝不读业务表行数据；
- 能力缺失降级：慢查询源全不可用 → CapabilityMissing 快照（非错误、正常间隔重试不退避），
  供上层提示"开启 pg_stat_statements / dbe_perf"；
- 两通道同构：同一 datasource 经 Direct 或 Connector 采同 kind，快照结构一致（字段集合
  与语义；值因时刻可异），集成测试固化；
- 尺寸有界：TopN / MaxTables / 文本截断 / SnapshotMaxBytes 多层上限，超限截断置
  Truncated，绝不无界上报；
- 兼容性：Snapshot/SnapshotSink/RowQuerier 是 spec-1.5/1.10/1.11 稳定契约；spec-1.3 的
  Metric/Batch/Sink/Querier **零改动**，指标采集行为不变。

## §4 测试用例

| # | 用例 | 目的 |
|---|---|---|
| T1 | slowlog 目录对启用 pg_stat_statements 的真 PG 可执行：TopN 上限、按累计耗时降序、字段齐备 | 目录有效性 |
| T2 | 慢查询条目值合理域（calls≥1、mean_ms≥0、text 非空）且文本为规范化形态（含占位符） | 值与形态合理性 |
| T3 | 向被采库执行含唯一字符串字面量的语句 → 上报文本不含该字面量 | AD-3 字面量防线 |
| T4 | pg_stat_statements 缺失 → CapabilityMissing 快照（成功路径、无错误码、不退避） | 能力降级 |
| T5 | 预置表/索引 → schema 快照含列/索引/大小/行估算；排除系统 schema；超 MaxTables 截断+标记 | schema 快照正确性 |
| T6 | config 快照含 max_connections/shared_buffers 等关键项且值非空 | config 快照正确性 |
| T7 | 同一实例 Direct/Connector 采同 kind → 快照结构一致 | 通道无关 |
| T8 | 未知 kind：collect API 400 + connector 白名单拒绝回 AR_COLLECT_UNSUPPORTED_KIND | AD-9 白名单双侧拒绝 |
| T9 | 快照探针执行期无任何 INSERT/UPDATE/DDL（只读断言） | 只读契约 |
| T10 | 慢查询文本超长 → 截断至 QueryTextMaxLen + 条目 Truncated 标记 | 尺寸有界 |
| T11 | 调度器：datasource×kind 独立循环、分 kind 间隔生效、datasource 删除即停全部 kind 循环 | 调度正确性 |
| T12 | Connector DataUpload kind=slowlog/schema/config → gateway SnapshotSink 收讫（端到端） | Connector 通道端到端 |
| T13 | 每个新错误码 ≥1 触发 | 错误码纪律 |

## §5 与现有代码的 contract

- 修改：proto/errors.json（+2 码，生成物同步）、libs/metrics（**新增** snapshot 模型/目录/
  探针文件，既有文件零破坏）、console/internal/collector（kind 维度）、console directconn
  适配（+RowQuerier 实现）、connector/internal/dbprobe（+3 指令类型处理）、gateway
  （collect API kind + SnapshotSink 转发）、.env.example / Helm values（新间隔）、
  dev PG（启用 pg_stat_statements：容器参数 + CREATE EXTENSION）、dev-verify（快照断言）；
- 不动：proto session.proto（DataUpload.kind 为 string，无 proto 变更）、spec-1.3
  Metric/Batch/Sink/Querier、spec-1.1 datasources schema、spec-1.2 会话/凭据边界；
- 对后续：Snapshot 是 spec-1.5 落库入口与 spec-1.11 慢查询分析输入；spec-1.6 脱敏规则
  引擎挂 Connector 上报前，本 spec 的编译期防线届时升级为可配规则。

## §6 风险

| 风险 | 概率 | 缓解 |
|---|---|---|
| pg_stat_statements 未安装/未预加载（原生 PG 默认不启用） | 高 | 能力探测 + CapabilityMissing 结构化降级（T4）；dev/CI PG 启用扩展实测成功路径；接入文档指引客户开启 |
| openGauss dbe_perf 视图字段/权限（monadmin）与原生 PG 差异 | 中 | 源候选链每源独立列映射与最低版本标注；无权限/缺视图走降级不崩；openGauss 真机双验在 spec-1.16 验收环境执行 |
| 慢查询文本泄漏敏感字面量（违反 AD-3） | 低/致命 | 只采规范化源、显式禁 pg_stat_activity.query（§1.2/§3）；T3 字面量注入断言；截断上限；review 逐条目录 SQL 核对 |
| 大库表结构快照过大（万表/宽表）超帧限或拖慢采集 | 中 | MaxTables=500 按大小降序 + SnapshotMaxBytes=512KB 双上限（Truncated 标记）；meta 低频 3600s；分页全量列 Stage 2 |
| 元数据查询在大库上慢（pg_total_relation_size 逐表开销、锁竞争） | 中 | 单快照在途 + 低频 + 目录 SQL 内嵌 LIMIT；随连接超时配置兜底；性能敏感路径留基线数据（规则 4） |
| 指令通道扩展被误用为任意 SQL 面（违反 AD-9） | 低/致命 | 指令 payload 零 SQL、kind 白名单双侧校验（T8）；SQL 全编译期目录；review 断言指令 payload 结构 |

## §7 DoD

- [ ] D1-D5 就位，T1-T13 全过（集成入 CI）
- [ ] slowlog 成功路径对启用 pg_stat_statements 的真 PG 实测；缺失降级路径实测（T4）
- [ ] 字面量防线实证（T3）+ 只读契约（T9）+ 指令零 SQL/白名单双侧拒绝（T8）
- [ ] 两通道同构（T7）+ Connector 端到端（T12）
- [ ] 探针/目录为共享代码，两通道仅提供 RowQuerier（无实现漂移）
- [ ] spec-1.3 既有契约零破坏（Metric/Batch/Sink/Querier 原样，指标采集回归绿）
- [ ] 新错误码注册 + 触发用例 + 生成物同步
- [ ] 覆盖率合并口径达标（libs/metrics、console、connector、gateway ≥80%）
- [ ] .env.example / Helm values / dev PG 扩展启用 / dev-verify 同步（schema+config 成功断言；slowlog 按 dev PG 能力断言对应路径）
- [ ] 上限常量与分 kind 间隔为受控口径，变更须修订本 spec
- [ ] specs 索引与 roadmap 进度表更新；CHANGELOG Unreleased 追加
- [ ] commit 格式合规，独立 PR

## §8 Q&A

**Q1 慢查询数据源：A. 统计视图（pg_stat_statements / dbe_perf）规范化聚合（★推荐） B. 日志文件解析（log_min_duration_statement） C. pg_stat_activity 周期采样**
推荐 A：SQL 通道可达（两通道同构成立）；文本天然规范化（字面量占位，AD-3 最友好）；
聚合统计（calls/total/mean/max）恰是 spec-1.11 分析输入。B 需文件系统访问面（改变
Connector 部署形态假设）且原始日志含字面量；C 是瞬时态采样、漏检率高、文本带字面量。

**Q2 快照载荷建模：A. 每 kind 强类型模型 + Snapshot 信封（★推荐） B. 通用 rows []map[string]string**
推荐 A：下游契约稳定（spec-1.5 schema 定版、spec-1.11 字段可依赖），字段白名单在类型层
（AD-3 防线构造期生效）。B 灵活但把校验与语义推给每个消费方，字段漂移无编译期防线。

**Q3 指令类型组织：A. 每 kind 独立指令类型 PROBE_SLOWLOG/PROBE_SCHEMA/PROBE_CONFIG（★推荐） B. 单指令 PROBE_SNAPSHOT 带 kind 参数**
推荐 A：指令类型即白名单（AD-9），gateway/connector 逐类型显式鉴别、未知类型天然拒绝、
审计粒度对齐动作语义。B 省两个常量但白名单校验内移到 payload 解析后，拒绝面变窄。

**Q4 采集频率：A. 分 kind 默认（slowlog 300s、schema/config 3600s，可配 + 下限护栏）（★推荐） B. 与指标同频 60s C. 仅手动触发**
推荐 A：慢查询统计是累积视图，5min 粒度足够；表结构/配置变更低频，1h 快照成本低。
B 元数据查询较重（逐表 size），60s 对大库是无谓负载；C 失去周期基线，spec-1.10 巡检
无数据可读。

**Q5 代码落位：A. 扩展 libs/metrics（snapshot*.go 并列文件）（★推荐） B. 新包 libs/snapshot**
推荐 A：快照复用同一双通道传输/Sink/测试基建，拆包会复制管线且 connector/console 依赖面
翻倍；包名承载"采集"域的沿革在包 doc 注明，Stage 2 类型膨胀时再拆不迟。B 命名洁癖收益
小于管线重复成本。

**Q6 配置采集范围：A. pg_settings 全量快照（★推荐） B. 白名单子集（~30 关键项）**
推荐 A：全量约 350 项×短字段，payload 数十 KB（远低于上限），诊断价值最大（spec-1.12
诊断对话可查任意项）；pg_settings 本身不含凭据类值。B 有白名单维护成本，漏关键项时
诊断断档；若发现引擎特例含敏感值，以排除表处理（受控口径追加）。

## §9 实施计划

| 步骤 | 内容 | 估时（评审轮次口径） |
|---|---|---|
| 1 | D1 快照模型 + 三类目录 + T1/T6（先立目录，对启用扩展的真 PG 验 SQL） | 1-2 轮 |
| 2 | D2 快照探针 + RowQuerier + T2/T3/T4/T9/T10 | 1-2 轮 |
| 3 | D4 Direct 通道（directconn RowQuerier + SnapshotSink）+ T5 | 1 轮 |
| 4 | D4 Connector 通道（dbprobe 三指令 + DataUpload kind + gateway 转发）+ T8/T12 | 1-2 轮 |
| 5 | D3 调度 kind 维度 + T11 + 同构 T7 + dev PG/dev-verify/DoD 收尾 | 1 轮 |

## §10 后续 spec 关联

- spec-1.5：实现 SnapshotSink 落库（slowlog 时序/元数据关系表的存储建模归其定版）；
- spec-1.6：脱敏规则引擎挂 Connector 上报前——慢日志文本是主要吃重对象，本 spec 的
  编译期防线（规范化源 + 截断）升级为可配规则；
- spec-1.10：巡检报告读 config/schema 快照；spec-1.11：慢查询分析消费 SlowQueryEntry；
- spec-1.16：openGauss 真机双验（dbe_perf 源候选）在 Stage 1 验收环境执行；
- Stage 2：schema 全量分页、多库目标映射、运营可配目录沿 spec-1.3/1.4 既有扩展点演进。

## §11 实施 changelog（frozen 后追加，不改上文）

- **2026-08-13 实施完成**：
  - **真机校准（og5，openGauss-lite 5.0.3）**：§2.2 的 dbe_perf 源按文档写的列名
    实测有两处不存在——**无 `avg_elapse_time`**（均值改由 `total_elapse_time /
    NULLIF(n_calls,0)` 算出）、**无库名列**（`database` 字段留空，不编造）。该视图的
    `query` 列实测已规范化（字面量为 `?`），§3 的 AD-3 前提在真机成立。
  - **同批验出两处本可漏网的 bug**：①表结构查询的 `ORDER BY size_bytes` 落在
    `::text` 输出别名上，PG 按字典序排（实测 99311616 > 974848），改
    `ORDER BY t.size_bytes` 走数值序；②**spec-1.3 遗留**：复制延迟指标在 openGauss
    直接报错（该发行版承 PG 9.2 血统，只有 `pg_last_xlog_receive_location` 一族），
    spec-1.3 DoD 写的"openGauss 双验"实际从未做成。`CatalogEntry` 增 `AltSQL`
    方言回退（主 SQL 执行报错时改用），补齐该验证。
  - §2.3 索引列改从 `pg_get_indexdef` 的 DDL 解析（深度感知的逗号切分），而非
    展开 `indkey`——openGauss 缺 `LATERAL`/`WITH ORDINALITY`。
  - Direct/Connector 两侧 `RowQuerier` 均把列值统一字符串化：快照跨方言取回的是
    异构标量，字符串是唯一无损且无需逐列类型表的中间形态。
  - dev-verify 修掉一处隐蔽 flake：`kubectl logs | grep -q` 在 `pipefail` 下因
    SIGPIPE 误判失败，**日志越多越容易触发**（即采集越正常越容易挂）。
  - 覆盖率合并口径达标：connector 87.3%、console 82.2%、gateway 81.5%、
    libs-metrics 84.7%。T1-T13 全过；dev-verify ALL PASS（三类快照心跳可见）。
  - openGauss 集成用例以 `AIRUSH_OPENGAUSS_*` 环境变量接外部实例，未设即跳过——
    需要 CI 不具备的外部依赖，本地/验收环境按 spec-1.16 执行。
