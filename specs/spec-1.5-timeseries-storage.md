# spec-1.5 数据接入层与时序存储

> **frozen** — user approve 2026-08-14（§8 Q1-Q7 **全采 ★**；新增系统服务依赖
> TimescaleDB 扩展一并批准；无新增 Go module，无 proto 变更）

## Header / 元数据

- **位置**：Stage 1 采集组收口件（roadmap 序：1.3 → 1.4 → **1.5**）；前置 spec-1.3（指标探针 /
  `metrics.Sink`）、spec-1.4（快照探针 / `metrics.SnapshotSink`）、spec-1.1（`datasources` 表 +
  租户上下文中间件 + repo 基座）、spec-0.6（迁移框架与租户表模板）；被 spec-1.10（巡检报告）、
  spec-1.11（慢查询分析）、spec-1.12（诊断对话）、spec-1.13（控制台图表）消费——**这四个 spec
  的全部数据来源就是本 spec 落的三张表**；
- **上游决策**：
  - **AD-7**（指标时序存储 = TimescaleDB，启用列存压缩）；
  - **AD-10**（多租户隔离由数据库强制；2026-08-14 修订后允许**等效形态**——本 spec 是
    等效形态的**首个也是唯一**使用者，须逐表登记 + 四项集成用例）；
  - **AD-3**（只落结构化数据，零业务行数据——落库层是最后一道编译期防线）；
- **核心定位**：把 spec-1.3/1.4 产出的采集数据从内存 `BufferSink` 换成持久化存储，并给出
  下游 skill / 控制台的**读接口**。同时定版**两层指标命名规范**（`db.*` 规范 + `引擎.*` 特有），
  让 Stage 3 接入 MySQL / 达梦时不必改表、不必改 skill；
- **表数收敛承诺**：采集侧表数**固定为 3 张**。往后新增采集能力（等待事件、索引使用、
  表膨胀、复制槽……）与新增引擎，一律只加**编译期目录常量**，零 DDL、零迁移。
  这条是本 spec 最重要的设计目标，§2 的泛化设计全部服务于它；
- **依赖审批（规则 5 硬门槛 #4）**：**新增一个系统服务依赖——TimescaleDB 扩展**
  （PG 插件形态，AD-7 既定，非新决策）。部署镜像由 `postgres:16` 换为
  `timescale/timescaledb:2.x-pg16`。**无新增 Go module**：复用既有 pgx/pgxpool。
  **无 proto 变更**；
- **实测支撑**：本 spec 的存储布局与隔离方案均有实测脚本，非纸面推演——
  `deploy/scripts/probe-timescale-rls.sh`（压缩与 RLS 互斥）、
  `probe-timescale-rls2.sh`（等效隔离四项）、`probe-series-final.sh`（三种布局体积与查询代价）；
- **决策日期**：2026-08-14 起草并 approve（§8 全 ★）。

---

## §1 范围

### §1.1 包含（Deliverables）

| # | Deliverable | 文件清单 | 估算 | 说明 |
|---|---|---|---|---|
| **D1** | 存储 schema 与迁移 | `console/migrations/0004_collected_timeseries.up.sql` / `.down.sql` | ~380 LOC | `collected`/`tsdb` 两 schema、3 张表、hypertable、压缩与保留策略、2 层连续聚合、隔离视图与授权（AD-10 等效形态落地点） |
| **D2** | 采集目录规范化（两层命名） | `libs/metrics/catalog.go`（改）、`libs/metrics/series.go`（新）、`libs/metrics/snapshot_catalog.go`（改） | ~260 LOC | `db.*` 规范指标词表 + `引擎.*` 特有指标；每条目录声明 unit / entity 语义（AD-3 白名单升级为目录声明） |
| **D3** | 时序 Sink（写路径） | `console/internal/tsstore/sink.go`、`series.go`、`snapshot.go`、`entity.go` | ~430 LOC | 实现 `metrics.Sink` + `metrics.SnapshotSink`；批量写、实体 upsert、快照内容哈希去重、租户上下文透传 |
| **D4** | 查询面（读路径） | `console/internal/tsstore/query.go`、`console/internal/httpapi/collected.go` | ~360 LOC | 指标区间/聚合查询、慢查询 Top N、快照当前版本与版本链；供 spec-1.10/1.11/1.13 消费的稳定契约 |
| **D5** | 接线与部署 | `gateway/internal/consoleclient/upload.go`（新）、`console/internal/httpapi/ingest.go`（新）、`gateway/cmd/gateway/server.go`（改）、`console/cmd/console/*.go`（改）、`deploy/charts/airush/templates/storage-builtin.yaml`（改）、`values.yaml`（改） | ~290 LOC | gateway 上报通道（§8 Q5）、console 侧接线、TimescaleDB 镜像与扩展初始化 |
| **D6** | 测试与验证 | `console/internal/tsstore/*_test.go`、`*_integration_test.go`、`deploy/scripts/dev-verify.sh`（改） | ~470 LOC | 含 AD-10 等效形态四项隔离用例（硬性）、压缩共存用例、dev-verify 端到端断言 |

合计估算 ~2190 LOC。

### §1.2 不包含（每条带理由）

| # | 不包含 | 理由 |
|---|---|---|
| 1 | `datasource_relations` 拓扑关系边表 | Stage 1 不做集群/容灾管理；现模型（`datasource_groups` + `group_role`）扁平分组对主备/集群够用，表达不了组间关系（容灾配对、级联复制、双活）。现在建 = 照想象写表，大概率写歪。已登记 roadmap §4.4，Stage 3/4 真做时按届时认知定型 |
| 2 | DDL 事件级审计（谁、何时、执行了什么语句） | 本 spec 的表结构变更历史是**采集期内容哈希比对**得出的，粒度 = 采集周期（1h），只知"变了什么"不知"谁改的"，且一小时内加了又删会完全看不见。要事件级需 DDL 触发器 / 逻辑解码 / binlog，是另一套机制与另一套安全模型 |
| 3 | 采集策略可配置（频率、阈值、巡检规则） | 现为编译期常量（spec-1.4：metrics 60s / slowlog 300s / meta 3600s）。多引擎 + 客户定制才需要配置表，归 spec-3.3（引擎特有巡检包）/ spec-3.9（巡检调度中心）。Stage 1 建策略表同样是照想象写 |
| 4 | 数据脱敏 | 归 spec-1.6（客户侧脱敏规则引擎）。本 spec 落库的慢查询文本是**服务端规范化产物**（字面量已为 `$N`/`?`），脱敏是在此之上的二次加固，位置在 Connector 侧而非落库侧 |
| 5 | 告警、阈值判定、异常检测 | 落库层只负责忠实存储。"什么算异常"是 skill 层判断（spec-1.10 巡检报告 / spec-1.12 诊断对话），且判断规则随引擎而异 |
| 6 | 跨租户聚合与平台大盘 | AD-10 隔离视图天然按租户过滤，平台级视图需要另一套受控角色与审计口径。归 Stage 4（`docs/ui-design-brief.md` 已登记「平台大盘」仅平台运营角色可见） |
| 7 | MySQL / 达梦 / TiDB 的采集目录 | 归 Stage 3（spec-3.1/3.2/3.3）。**但本 spec 的两层命名规范就是为它们设计的**——届时只加目录常量，不改本 spec 落的任何表 |
| 8 | 数据导出 / 归档到对象存储 | 保留期到点即 `drop_chunks`。长期归档属成本优化，归 Stage 4（spec-4.2 报表中心一并考虑） |

### §1.3 例外说明

无。本 spec 严格在 Stage 1 内，前置 spec-1.3/1.4 均已 shipped。

---

## §2 接口设计

### §2.1 存储布局全景

```
collected schema（对外可见面：视图 + 普通表）
├── series          ← 视图，指向 tsdb.series（隔离）
├── series_5m       ← 视图，指向 tsdb.series_5m（隔离）
├── series_1h       ← 视图，指向 tsdb.series_1h（隔离）
├── entities        ← 普通租户表，标准 RLS 模板
└── snapshots       ← 普通租户表，标准 RLS 模板

tsdb schema（基表，airush_app 无 USAGE —— 连表名都引用不到）
├── series          hypertable + 列存压缩
├── series_5m       连续聚合
└── series_1h       连续聚合（从 5m 再卷）
```

**为什么自然名给视图**：`collected.series` 是视图，基表藏在 `tsdb` 里。谁凭习惯写
`SELECT ... FROM collected.series` 拿到的就是隔离视图；想碰基表得**主动**写 `tsdb.` 前缀，
而且写了也会被拒（无 schema USAGE）。安全的那条是默认的那条。

**为什么采集数据单独一个 schema**：控制面 PG 自己也是 PG。`snapshots`、`series` 这种名字
放在 `public` 里，半年后极易被误读为"平台自己的监控数据"。`collected.` 前缀让出处自带在名字上
（§8 Q&A 未列——此为命名惯例，非架构决策）。

### §2.2 表 1：`tsdb.series` —— 读数流水

```sql
CREATE TABLE tsdb.series (
    tenant_id     uuid             NOT NULL,
    datasource_id uuid             NOT NULL,
    series_name   text             NOT NULL,  -- 'db.connections.active' / 'db.slowlog.total_ms'
    entity_id     text             NOT NULL DEFAULT '',  -- 实体标识；无实体的指标为空串
    value         double precision NOT NULL,
    at            timestamptz      NOT NULL
);
SELECT create_hypertable('tsdb.series', 'at', chunk_time_interval => interval '1 day');

ALTER TABLE tsdb.series SET (
    timescaledb.compress,
    timescaledb.compress_segmentby = 'tenant_id, datasource_id, series_name, entity_id',
    timescaledb.compress_orderby   = 'at DESC'
);
SELECT add_compression_policy('tsdb.series', interval '7 days');
SELECT add_retention_policy  ('tsdb.series', interval '14 days');

CREATE INDEX series_lookup_idx
    ON tsdb.series (tenant_id, datasource_id, series_name, at DESC);
```

**这一张表承载全部读数流水**——指标、慢查询统计、以及未来所有「实体 + 数值 + 时间」形态的
采集产物。慢查询的 5 个度量拆成 5 条 series：

| 采集产物 | series_name | entity_id |
|---|---|---|
| 活跃连接数 | `db.connections.active` | `''` |
| 慢查询调用次数 | `db.slowlog.calls` | 该 SQL 的实体 ID |
| 慢查询累计耗时 | `db.slowlog.total_ms` | 同上 |
| （未来）索引扫描次数 | `db.index.scans` | 索引名 |
| （未来）表膨胀字节 | `db.table.bloat_bytes` | 表名 |

**故意没有的列**（每条都是有意的取舍，不是遗漏）：

| 列 | 为什么不建 |
|---|---|
| `id` 主键 | 时序表无单行寻址需求；每行多 16 字节且压缩不掉 |
| `unit` | 编译期常量，随 `series_name` 唯一确定，存了就是每行重复 |
| `engine` / `engine_family` | 是数据源属性，读时 join `datasources`。存进来是每行重复的死数据（§8 Q&A 未列——代价是数据源删除后历史读数查不到引擎类型，可接受：数据源删除时其采集数据一并按 FK 语义清理） |
| `entity_label`（SQL 文本等） | 归 `collected.entities` 字典表。实测：内联使压缩后体积从 5384 kB 涨到 8976 kB（+67%），因为一条 SQL 有 5 个 series_name 就要存 5 份 |
| `labels jsonb` | jsonb 在列存里 segment 不了，压缩率断崖；且是 AD-3 的口子（任意键值） |
| `catalog_version` | 属批次元数据不属样本。语义变更用**新 series_name**，不用旧名改义 |

**实测代价**（`probe-series-final.sh`，5 数据源 × 50 SQL × 7 天 × 288 次/天）：

| 布局 | 行数 | 压缩后 | Top10 查询 | 展开 5 度量 |
|---|---|---|---|---|
| 强类型专表（每类一张） | 504k | 3240 kB | 0.15 ms | 0.18 ms |
| **本方案**（通用 + 实体字典） | 2.52M | **5384 kB** | 0.21 ms | 0.29 ms |

泛化代价 = **1.66× 存储**（100 数据源跑一年约多 2.2 GB），换取表数永久固定。
最常用的 Top N 查询基本无差；pivot 查询慢 1.5× 但都在亚毫秒。

### §2.3 表 2：`collected.entities` —— 实体字典

```sql
CREATE TABLE collected.entities (
    tenant_id     uuid        NOT NULL REFERENCES tenants(id),
    datasource_id uuid        NOT NULL,
    entity_kind   text        NOT NULL,   -- 'query' | 'index' | 'table' | 'waitevent' | ...
    entity_id     text        NOT NULL,   -- 与 tsdb.series.entity_id 同值
    label         text        NOT NULL,   -- 规范化 SQL / 索引名 / 表名
    native_id     text        NOT NULL DEFAULT '',  -- 引擎原生标识（queryid/unique_sql_id），供排障
    attributes    jsonb       NOT NULL DEFAULT '{}'::jsonb,  -- 引擎特有属性；键须经目录声明
    first_seen_at timestamptz NOT NULL,
    last_seen_at  timestamptz NOT NULL,
    PRIMARY KEY (tenant_id, datasource_id, entity_kind, entity_id),
    FOREIGN KEY (tenant_id, datasource_id) REFERENCES datasources(tenant_id, id) ON DELETE CASCADE
);
ALTER TABLE collected.entities ENABLE ROW LEVEL SECURITY;
ALTER TABLE collected.entities FORCE  ROW LEVEL SECURITY;
CREATE POLICY tenant_isolation ON collected.entities
    USING (tenant_id = NULLIF(current_setting('app.tenant_id', true), '')::uuid);
GRANT SELECT, INSERT, UPDATE ON collected.entities TO airush_app;
```

**这张表是双重收益的**，不是纯存储优化：

1. **省 40% 存储**（实测 8976 → 5384 kB）；
2. **给"实体"一个稳定挂载点**——`first_seen_at` 直接回答"这条慢 SQL 是这周才冒出来的"
   （DBA 最关心的告警之一），而这在纯时序表里要扫全历史才能得出；后续的人工忽略标记、
   agent 分析结论也都挂在这里而非某一时刻的读数上。

它是**普通表走标准 RLS 模板**（spec-0.6 §2.2）——不随时间增长，只随"出现过的不同实体数"
增长，不需要压缩，因而不需要 AD-10 等效形态。

### §2.4 表 3：`collected.snapshots` —— 慢变状态

```sql
CREATE TABLE collected.snapshots (
    tenant_id          uuid        NOT NULL REFERENCES tenants(id),
    id                 uuid        NOT NULL DEFAULT gen_random_uuid(),
    datasource_id      uuid        NOT NULL,
    kind               text        NOT NULL CHECK (kind IN ('schema', 'config')),
    source             text        NOT NULL DEFAULT '',
    capability_missing boolean     NOT NULL DEFAULT false,
    truncated          boolean     NOT NULL DEFAULT false,
    catalog_version    int         NOT NULL,
    content_hash       text        NOT NULL,   -- sha256(payload 规范化序列化)
    payload            jsonb       NOT NULL,   -- ≤512KB（spec-1.4 SnapshotMaxBytes 已强制）
    collected_at       timestamptz NOT NULL,   -- 该内容最近一次被观察到
    created_at         timestamptz NOT NULL,   -- 该版本首次出现
    superseded_at      timestamptz,            -- NULL = 当前生效版本
    PRIMARY KEY (tenant_id, id),
    FOREIGN KEY (tenant_id, datasource_id) REFERENCES datasources(tenant_id, id) ON DELETE CASCADE
);
ALTER TABLE collected.snapshots ENABLE ROW LEVEL SECURITY;
ALTER TABLE collected.snapshots FORCE  ROW LEVEL SECURITY;
CREATE POLICY tenant_isolation ON collected.snapshots
    USING (tenant_id = NULLIF(current_setting('app.tenant_id', true), '')::uuid);
GRANT SELECT, INSERT, UPDATE ON collected.snapshots TO airush_app;

-- 每个 (数据源, kind) 至多一个当前版本
CREATE UNIQUE INDEX snapshots_current_idx
    ON collected.snapshots (tenant_id, datasource_id, kind) WHERE superseded_at IS NULL;
CREATE INDEX snapshots_history_idx
    ON collected.snapshots (tenant_id, datasource_id, kind, created_at DESC);
```

**只在内容变化时才插新行**：

```
08-01 10:00  hash=abc123  首次 → 插一行（当前版本）
08-01 11:00  hash=abc123  没变 → 仅 UPDATE collected_at
   ...（连续 20 小时无变更，零新增行）
08-02 07:00  hash=def456  变了 → 旧行 superseded_at=07:00，插新行
```

于是这张表天然就是**变更历史**，而不是 24 份一模一样的快照。
`kind='slowlog'` **不入此表**——慢查询每次采集内容必变，哈希去重完全失效，且它需要
按实体做趋势，形态属读数流水，走 `tsdb.series`。

### §2.5 隔离层（AD-10 等效形态，本 spec 唯一使用者）

```sql
CREATE SCHEMA tsdb;
REVOKE ALL ON SCHEMA tsdb FROM PUBLIC;
-- 关键：**不** GRANT USAGE ON SCHEMA tsdb TO airush_app —— 双锁第一道

CREATE SCHEMA collected;
GRANT USAGE ON SCHEMA collected TO airush_app;

CREATE VIEW collected.series
    WITH (security_barrier = true, check_option = cascaded) AS
SELECT tenant_id, datasource_id, series_name, entity_id, value, at
  FROM tsdb.series
 WHERE tenant_id = NULLIF(current_setting('app.tenant_id', true), '')::uuid;
GRANT SELECT, INSERT ON collected.series TO airush_app;
-- collected.series_5m / series_1h 同形态（只 GRANT SELECT）
```

**四项准入门槛**（AD-10 修订时定的硬要求，逐条对应 §4 用例）：

| # | 门槛 | 用例 |
|---|---|---|
| ① | 压缩启用下经视图读，只见本租户 | T7 |
| ② | 无租户上下文 → 0 行 fail-closed（非报错） | T8 |
| ③ | 应用角色绕过视图直读基表被拒 | T9 |
| ④ | 伪造他人 tenant_id 写入被拒 | T10 |

> ④ 在 `probe-timescale-rls2.sh` 初验时**没拦住**——`security_barrier` 只管读不管写，
> 补 `check_option = cascaded` 才堵上。这就是把"四项"写成硬门槛而不是一句"用视图也行"
> 的理由：等效形态有不显眼的缺口，不逐项验证就会漏。

### §2.6 两层指标命名规范（为 Stage 3 多引擎预留）

```
db.*                规范指标：跨引擎语义一致、单位固定。skill 优先用这层
pg.* / mysql.* / dm.*   引擎特有：只在该引擎存在。深度诊断用
```

引擎差异在**采集时**消化，不留给 skill：

| 规范指标 | 单位 | openGauss/PG 实现 | （Stage 3）MySQL 实现 |
|---|---|---|---|
| `db.replication.lag_seconds` | seconds | `now() - pg_last_xact_replay_timestamp()` | `Seconds_Behind_Master` 直取 |
| `db.cache.hit_ratio` | ratio | `blks_hit/(hit+read)` | buffer pool 读命中率 |
| `db.connections.active` | count | `pg_stat_activity WHERE state='active'` | `Threads_running` |
| `pg.replication.lag_bytes` | bytes | WAL 字节差 | **无**（PG 特有，不硬凑） |

**"没有的就是没有"是正常路径**，与 spec-1.4 已实现的 `CapabilityMissing` 同一语义。
目录条目新增声明字段：

```go
type CatalogEntry struct {
    Name string        // 两层命名后的 series_name
    Unit string
    SQL  string
    AltSQL string
    Nullable bool
    // EntityKind 声明本条 series 是否带实体维度、实体是什么。
    // 空串 = 无实体（entity_id 恒为空）。**非空时必须在此声明**——
    // 未声明的 entity 一律拒绝入库，这是 AD-3 从"label 键白名单"升级来的防线。
    EntityKind string
}
```

### §2.7 写路径接口

```go
// TimescaleSink 同时实现 metrics.Sink 与 metrics.SnapshotSink（spec-1.3/1.4 契约不变）。
type TimescaleSink struct { pool *pgxpool.Pool }

var (
    _ metrics.Sink         = (*TimescaleSink)(nil)
    _ metrics.SnapshotSink = (*TimescaleSink)(nil)
)

func (s *TimescaleSink) Publish(ctx context.Context, batch metrics.Batch) error
func (s *TimescaleSink) PublishSnapshot(ctx context.Context, snap metrics.Snapshot) error
```

**spec-1.3/1.4 的 Go 侧强类型契约完全不变**——`metrics.Batch`、`metrics.Snapshot`、
`SlowQueryEntry` 结构体照旧。强类型 → 通用存储的转换**收口在 sink 一处**（见 §8 Q&A 未列项：
此为 spec-1.4 §8 Q2「强类型而非通用 rows」的调和——那条决策管的是 Go 侧与 API 契约，
本 spec 的泛化只在存储层）。

### §2.8 读路径接口

```go
// 指标区间查询：自动按 range 选层（≤14d 走 raw，≤90d 走 5m，更长走 1h）。
func (q *Querier) SeriesRange(ctx, datasourceID, seriesName string, from, to time.Time, step time.Duration) ([]Point, error)

// 慢查询 Top N：按某个 series（如 db.slowlog.total_ms）排序，带出实体 label。
func (q *Querier) TopEntities(ctx, datasourceID, seriesName string, from, to time.Time, n int) ([]RankedEntity, error)

// 快照当前版本 / 版本链。
func (q *Querier) LatestSnapshot(ctx, datasourceID, kind string) (*Snapshot, error)
func (q *Querier) SnapshotHistory(ctx, datasourceID, kind string, limit int) ([]SnapshotMeta, error)
```

### §2.9 配置项

| 键 | 默认 | 说明 |
|---|---|---|
| `AIRUSH_TS_RAW_RETENTION` | `14d` | 原始点保留期 |
| `AIRUSH_TS_5M_RETENTION` | `90d` | 5 分钟聚合保留期 |
| `AIRUSH_TS_1H_RETENTION` | `400d` | 1 小时聚合保留期 |
| `AIRUSH_TS_COMPRESS_AFTER` | `7d` | 压缩延迟（须 > 迟到数据窗口） |
| `AIRUSH_TS_BATCH_MAX_ROWS` | `5000` | 单次写入批上限 |

---

## §3 行为契约

1. **租户上下文强制**：所有读写走 repo 基座既有的事务级 `SET LOCAL app.tenant_id`
   （spec-1.1 D3）。未设置时视图返回 0 行、写入被 `check_option` 拒——**fail-closed，不报错**，
   与 spec-0.6 §3 的 RLS 契约语义一致；
2. **重复上报幂等**：同一 `(datasource_id, series_name, entity_id, at)` 重复写入不去重
   （时序表无唯一约束，去重代价高于收益）。调度侧保证不重复；下游查询用聚合函数天然抗重复；
3. **实体先于读数**：`Publish` 在同一事务内先 upsert `entities` 再插 `series`，
   保证不出现"有读数无实体"的悬挂。事务失败整批回滚；
4. **快照哈希去重**：`content_hash` 相同仅 `UPDATE collected_at`；不同则旧行
   `superseded_at = now()` + 插新行，**同一事务**内完成（唯一索引保证至多一个当前版本）；
5. **CapabilityMissing 快照照常入库**：`capability_missing = true` 且 `payload = '{}'`。
   这是成功路径的结构化降级（spec-1.4 §2.3），下游据此提示"请开启 pg_stat_statements"；
6. **未声明实体拒绝入库**：目录未声明 `EntityKind` 的 series 携带非空 `entity_id` 时，
   sink 显式报错 `AR_SERIES_UNDECLARED_ENTITY`（**不静默丢弃**，规则 6）；
7. **连续聚合实时性**：开启 TimescaleDB real-time aggregation——未物化的最近数据由视图
   实时计算补上，控制台不会因刷新周期看到数据缺口；
8. **压缩与迟到数据**：压缩延迟（7d）远大于采集迟到窗口（分钟级）。若仍有数据落进已压缩
   chunk，TimescaleDB 2.x 支持插入但性能退化——sink 记 warn 日志与 metric，不失败；
9. **保留期到点即删**：`drop_chunks` 硬删，无归档（§1.2 #8）。删除不影响 `entities`
   与 `snapshots`（它们不受时序保留策略约束）；
10. **错误码**：新增 `AR_TS_WRITE_FAILED`、`AR_TS_QUERY_FAILED`、`AR_SERIES_UNDECLARED_ENTITY`，
    domain 新增 `TIMESERIES`（`proto/errors.json`）。

---

## §4 测试用例

### 单元

| # | 用例 | 目的 |
|---|---|---|
| T1 | Batch → series 行映射（含无实体指标 entity_id 为空） | 写路径转换正确 |
| T2 | Snapshot(slowlog) → 5 条 series + entities upsert | 慢查询拆 series 正确 |
| T3 | Snapshot(schema) 首次入库 → 当前版本 | 快照写路径 |
| T4 | 同 hash 二次入库 → 不新增行，仅更新 collected_at | 哈希去重 |
| T5 | 异 hash 入库 → 旧行 superseded、新行当前 | 变更历史成链 |
| T6 | 目录未声明 EntityKind 却带 entity_id → 报 `AR_SERIES_UNDECLARED_ENTITY` | AD-3 防线，显式拒绝非静默丢弃 |
| T11 | CapabilityMissing 快照入库且 payload 为空对象 | 降级路径 |
| T12 | 两层命名：`db.*` 与 `pg.*` 目录条目均可入库且 unit 由目录决定 | 命名规范 |
| T13 | 批量超 `BATCH_MAX_ROWS` → 分批写且整体事务语义保持 | 边界 |

### 集成（需真 TimescaleDB）

| # | 用例 | 目的 |
|---|---|---|
| **T7** | **压缩启用下经视图读，只见本租户** | **AD-10 门槛①** |
| **T8** | **无租户上下文 → 0 行（非报错）** | **AD-10 门槛② fail-closed** |
| **T9** | **airush_app 直读 `tsdb.series` 被拒** | **AD-10 门槛③** |
| **T10** | **经视图伪造他人 tenant_id 写入被拒** | **AD-10 门槛④（初验漏拦过的那条）** |
| T14 | 压缩 chunk 与隔离视图共存：compress_chunk 后 T7 仍成立 | 压缩与隔离不互相破坏 |
| T15 | 连续聚合 5m/1h 数值与原始点聚合一致 | 聚合正确性 |
| T16 | real-time aggregation：刚写入未物化的点可被 5m 视图查到 | 无数据缺口 |
| T17 | `SeriesRange` 按 range 自动选层（14d/90d/400d 三档） | 读路径选层 |
| T18 | ~~`TopEntities` 返回带 label 的 Top N，与手工聚合一致~~ **移交 spec-1.11**（2026-08-15 review：累计计数器需差分 + 目录 Kind，见 §11）；本 spec 端点显式 501 有用例 | 慢查询主查询路径 |
| T19 | 保留策略：`drop_chunks` 后 entities/snapshots 不受影响 | 保留期边界 |
| T20 | 数据源删除 → 其 entities/snapshots 级联清理 | FK 语义 |

### 端到端

| # | 用例 | 目的 |
|---|---|---|
| T21 | dev-verify：直连采集 → 落库 → 查询面返回非空指标与慢查询 Top N | 全链路 |
| T22 | dev-verify：Connector 通道采集 → 落库（走 §8 Q5 选定的上报路径） | 双通道等价 |

---

## §5 与现有代码的 contract

**动的**：

| 模块 | 改动 | 兼容性 |
|---|---|---|
| `libs/metrics/catalog.go` | 指标改两层命名 + 新增 `EntityKind` 字段 | **破坏性**：series_name 全改。此时尚无历史数据，代价最小（§8 Q3） |
| `gateway/cmd/gateway/server.go` | `BufferSink` → 上报通道（§8 Q5） | 内部装配，无对外契约变化 |
| `console/internal/collector` | sink 注入换实现 | 接口不变（仍是 `metrics.Sink`） |
| `deploy/charts/.../storage-builtin.yaml` | PG 镜像 → timescaledb，加扩展初始化 | 部署侧，dev/kind 环境需重建 |
| `proto/errors.json` | 新增 3 个错误码 + `TIMESERIES` domain | 追加，不改既有 |

**不动的**：

- `metrics.Sink` / `metrics.SnapshotSink` 接口签名——本 spec 只加实现；
- `metrics.Batch` / `Snapshot` / `SlowQueryEntry` 强类型模型——spec-1.4 §8 Q2 的强类型
  决策在 Go 侧完整保留；
- Connector 协议（`proto/connector/v1`）——采集与上报协议零变更；
- spec-0.6 §2.2 租户表模板——`entities`/`snapshots` 严格照抄；`tsdb.series` 是登记在案的
  **等效形态**（AD-10 修订允许，spec-0.6 §11 Changelog 已记）。

---

## §6 风险

| # | 风险 | 概率 | 后果 | 缓解 |
|---|---|---|---|---|
| R1 | `check_option` 逐行校验使批量写入显著退化 | **中** | 采集吞吐不达标 | §4 加基准用例，门槛：相对无视图直写退化 >30% 即触发 §8 Q2 备选（独立 ingest 角色）。**门槛写进 DoD，不达标不算完成** |
| R2 | 连续聚合物化落后，控制台最近数据出现缺口 | 中 | 用户以为采集挂了 | 开启 real-time aggregation（T16 固化）；`schedule_interval` 设 5min，`end_offset` 设 10min |
| R3 | 指标改名破坏 spec-1.3 已有单测与 dev-verify 断言 | **高** | CI 红 | 改名与断言同步一次改到位；grep 全仓 `pg.` 前缀字面量清点（已知落点：`catalog.go`、`probe_test.go`、`dev-verify.sh`） |
| R4 | TimescaleDB 扩展在 kind / CI 环境不可用或版本不符 | 中 | 集成测试全挂 | 镜像固定 `timescale/timescaledb:2.x-pg16`；迁移里 `CREATE EXTENSION IF NOT EXISTS timescaledb` 失败即硬报错（非静默跳过）；dev-verify 加扩展存在断言 |
| R5 | 隔离视图授权写漏一条 → fail-**open**（跨租户可见） | 低 | **P0 数据串租** | 四项集成用例（T7-T10）为硬门槛；迁移里显式 `REVOKE ALL ON SCHEMA tsdb FROM PUBLIC` 且**不**授 USAGE；review 逐条核对 §2.5 |
| R6 | 实体 ID 换成平台侧哈希后与 spec-1.4 上报的 `QueryID` 关联断裂 | 中 | 排障时对不上引擎侧统计 | `entities.native_id` 保留引擎原生标识；sink 层统一计算哈希，单点可审计（§8 Q4） |
| R7 | 快照 jsonb 频繁 TOAST 读写影响查询延迟 | 低 | 巡检 skill 变慢 | payload ≤512KB（spec-1.4 已强制）；查询面默认只取 meta，payload 按需单独取 |
| R8 | 泛化后 `entity_id` 成为高基数维度，压缩 segment 数爆炸 | 中 | 压缩率下降、查询变慢 | 目录声明 `EntityKind` 时须同时声明基数上限；慢查询已由 `SlowlogTopN=50` 封顶；新增实体类采集必须在 spec 中论证基数 |

---

## §7 DoD

- [x] D1-D6 全部交付，迁移 `up`/`down` 均可执行且 `up→down→up` 结果一致（spec-0.6 T2 语义）；
- [x] **AD-10 四项隔离用例 T7-T10 全绿**——任一不过即本 spec 不可合并（硬门槛）；
- [x] T14 证明压缩与隔离视图共存（AD-7 与 AD-10 冲突的最终闭环验证）；
- [x] R1 基准：批量写入相对无视图直写退化 ≤30%，实测数据记入 spec changelog；
- [x] 单元测试 T1-T6、T11-T13 全绿；集成 T14-T20 全绿；端到端 T21-T22 全绿；
- [x] 覆盖率：`console` ≥80%、`libs/metrics` ≥80%（CLAUDE.md 规则 4 后端门槛）；
- [x] 两层命名落地：`libs/metrics` 内不再有裸 `pg.` 前缀的**规范类**指标（引擎特有的保留）；
- [x] 三个新错误码入 `proto/errors.json` 且各有触发用例（规则 4：每个错误码有触发用例）；review 追加第 4 个 `AR_COLLECT_DATASOURCE_MISMATCH`，同样有替身单测 + 真库集成用例；
- [x] 可观测性（spec-0.9 三件套）：写入批数/行数/失败数 metric、查询延迟 histogram、
      压缩与保留策略执行日志；
- [x] dev-verify ALL PASS，含 T21/T22 两条新断言；
- [x] Helm 部署 TimescaleDB 镜像在 kind 环境起得来，扩展就绪断言通过；
- [ ] CI 全绿（含已接入的 openGauss 集成任务不受影响）；**← 唯一未闭项：分支尚未推送，
      本地 Mac 上单测/集成/覆盖率/dev-verify 均已全绿**
- [x] 文档同步：spec 状态、roadmap §8 进度表、CHANGELOG、`docs/2026-08-08-airush-platform-design.md`
      §2.5 存储矩阵补 `collected`/`tsdb` schema 说明。

---

## §8 Q&A（决策点）

### Q1：采集到的累计值，存原值还是存差分？

`pg_stat_statements` / `dbe_perf` 给的是**自实例启动以来的累计值**。

- **★ A. 存累计原值**，差分在查询侧用 TimescaleDB `counter_agg` 算。
  理由：忠实记录采集所见，实例重启导致的计数器回绕由 `counter_agg` 原生处理；
  存差分则重启那一刻会产生假的巨大负值/尖峰，要额外识别与修补，而**修补逻辑一旦写错，
  错误会永久写死在数据里无法回溯**。原值方案的错误只影响查询，可修。
- B. 写入时算差分存增量。省一次查询期计算，但引入不可逆的写入期判断。

> 选错要重灌历史数据，故列为 Q1。

### Q2：写入是否也走隔离视图？

- **★ A. 读写都走视图**（`check_option = cascaded`）。越权写入被数据库拒。
  理由：AD-10 等效形态的完整性依赖 ④，去掉写路径校验等于门槛只剩 3 项。
  代价是逐行 CHECK，故 R1 挂实测门槛（>30% 退化即回退到 B）。
- B. 另开 `airush_ingest` 角色，对 `tsdb.series` 只 INSERT 无 SELECT。零开销；
  写错租户不会被拦（构成数据污染），但因无 SELECT 权限**不构成数据泄露**。

### Q3：指标现在改两层命名，还是 Stage 3 接 MySQL 时再改？

- **★ A. 现在改**。理由：此时**尚无任何历史数据**，改名零迁移成本。Stage 3 再改要写数据迁移、
  要兼容两套名字、skill 侧要处理历史断层。代价是本 spec 要同步改 spec-1.3 的测试断言（R3）。
- B. 先按 `pg.*` 落库，Stage 3 统一改。推迟工作量但放大代价。

### Q4：实体 ID 用引擎给的还是平台算的？

- **★ A. 平台算 `sha256(规范化文本)[:16]`**。理由：引擎给的 `queryid`/`unique_sql_id`
  实例重启可能变、跨实例不可比、跨引擎语义不同；同一条 SQL 在主备两个实例上应当是**同一个实体**，
  只有内容哈希能做到。代价是每次采集多算 50 次哈希（微秒级，可忽略）。
  引擎原生标识存进 `entities.native_id` 供排障对照。
- B. 直接用引擎给的。省哈希计算，但实体身份随实例重启漂移，`first_seen_at` 失去意义。

### Q5：gateway 怎么把 Connector 上报的数据落库？

**现状**：gateway **完全没有 DB 连接**，只通过 HTTP 与 console 通信（`consoleclient`）。

- **★ A. gateway POST 给 console，console 落库**。
  理由：① gateway 是面向客户侧 Connector 的接入组件，给它 DB 访问会显著扩大被攻破后的
  爆炸半径；② console 已有租户上下文中间件与 repo 基座（`SET LOCAL app.tenant_id`），
  复用它比在 gateway 重建一套正确性高得多；③ 吞吐无压力——1000 个数据源 60s 采集 = 17 req/s。
  代价是多一跳，且 console 成为写入路径的单点（可水平扩容缓解）。
- B. gateway 自带 pgxpool 直写。少一跳，但要在 gateway 复制一套租户上下文强制逻辑，
  且违背"接入层不碰持久化"的现有分层。

### Q6：连续聚合做几层？

- **★ A. 两层（5m + 1h）**。理由：60s 采集下，控制台画"最近 24 小时"要扫 1440 个原始点/指标，
  5 分钟层降到 288 个；"最近一年"必须走 1 小时层（否则 52 万点）。1 小时粒度看不出短时抖动，
  而抖动正是 DBA 要看的。
- B. 一层（只 1h）。少一套刷新策略要维护，但 24h 视图要么慢要么糙。

### Q7：三层保留期怎么定？

- **★ A. raw 14d / 5m 90d / 1h 400d**。理由：raw 覆盖"上周那次故障"的排查窗口；
  5m 覆盖季度环比；1h 400 天覆盖同比（跨年）。
- B. raw 30d / 5m 180d / 1h 730d。排查窗口更宽，存储约 2×。
- 依据：raw 14 天在 100 数据源规模下约 1.5 GB（由 §2.2 实测外推）。

---

## §9 实施计划

| # | 步骤 | 估时 |
|---|---|---|
| 1 | D1 迁移（含隔离视图与授权）+ T7-T10 四项隔离用例**先写** | 1 天 |
| 2 | D2 目录两层命名改造 + 同步 spec-1.3 断言（R3） | 0.5 天 |
| 3 | D3 写路径 sink + T1-T6/T11-T13 单测（TDD：测试先行） | 1 天 |
| 4 | D4 读路径查询面 + T17-T18 | 0.75 天 |
| 5 | D5 接线（Q5 选定路径）+ Helm/TimescaleDB 镜像 | 0.75 天 |
| 6 | D6 集成 T14-T16/T19-T20 + dev-verify T21-T22 + R1 基准 | 1 天 |
| 7 | 覆盖率补齐、code review、文档同步 | 0.5 天 |

总计 **5.5 天**。

> 步骤 1 把四项隔离用例排在最前，是因为它们一旦不成立，整个 AD-10 等效形态方案作废，
> 后面 5 天全是白干。**先证伪最贵的假设。**

---

## §10 后续 spec 关联

- **spec-1.6**（脱敏引擎）：慢查询文本落进 `collected.entities.label`，是脱敏的主要吃重对象；
- **spec-1.10**（巡检报告）：读 `snapshots` 当前版本 + `series` 区间聚合；
- **spec-1.11**（慢查询分析）：读 `TopEntities` + `entities.first_seen_at`（新出现的慢 SQL）；
- **spec-1.12**（诊断对话）：Stage 1 验收标准"回答『昨晚为什么慢』并引用采集数据"，
  引用的就是本 spec 三张表；
- **spec-1.13**（控制台图表）：消费 `SeriesRange` 的自动选层能力；
- **spec-1.15**（审计日志）：审计事件是另一套表，**不进** `collected` schema（那里只放采集来的
  客户库数据，平台自身数据不混入）；
- **spec-3.1/3.2/3.3**（MySQL/达梦/引擎巡检包）：按 §2.6 两层命名只加目录常量，
  **不改本 spec 落的任何表**——这是本 spec 表数收敛承诺的兑现点；
- **spec-3.9**（巡检调度中心）：采集策略从编译期常量搬到配置表（§1.2 #3）；
- **远期**（roadmap §4.4）：`datasource_relations` 拓扑边表（§1.2 #1）。

---

## §11 实施 Changelog（frozen 后追加，不重写正文）

| 日期 | 变更 |
|---|---|
| 2026-08-14 | **慢查询耗时类 series 改用秒，名字随之由 `db.slowlog.total_ms` 等改为 `db.slowlog.total_seconds` 等。** §2.2 示例表原写 `_ms`，与 §2.6 的规范层定位不自洽——`Unit` 词表只有 count/ratio/bytes/seconds，没有毫秒项。规范层的意义就是单位统一：PG 的 `pg_stat_statements` 给毫秒、MySQL 的慢日志给秒，换算在采集侧消化一次（`metrics.SlowQuerySeriesValues` 是唯一换算点），下游 skill 与图表永不必判断"这条是毫秒还是秒"。属实现期发现的 spec 文字缺陷，语义与 Deliverable 边界不变 |
| 2026-08-14 | **`pg.database.size_bytes` 改规范名时一并更名为 `db.storage.size_bytes`。** 该指标统计的是实例上全部库的总字节数，而 "database" 一词在 MySQL 里指 schema、在 openGauss 里指库，跨引擎歧义大，不适合做规范指标名的中段 |
| 2026-08-14 | **新增规范指标 `db.replication.lag_seconds`**（§2.6 表中已列出，本次实现）。PG 族用 `now() - pg_last_xact_replay_timestamp()` 换算，与 MySQL 的 `Seconds_Behind_Master` 语义对齐；原 `pg.replication.lag_bytes` 保留在引擎特有层（MySQL 无字节级延迟概念，不硬凑） |
| 2026-08-14 | **迁移文件组织受两条 TimescaleDB 约束定型**（实测，非推演）：① 连续聚合必须 `WITH NO DATA`——golang-migrate 整文件单次 Exec 构成隐式事务块，`WITH DATA` 报 `cannot run inside a transaction block` 且整批回滚；② `materialized_only` 必须显式设 `false`——2.13+ 默认为 `true`，那样刚写入未物化的点在视图里不可见，控制台会出现假数据缺口（对应 §3.7 与 T16） |
| 2026-08-15 | **R1 基准通过，§8 Q2 选项 A（写入经隔离视图）确认落地**：20000 行 × 3 轮取最小，经 `collected.series` 视图 499ms vs 直插 `tsdb.series` 基表 471ms，**退化 5.9%**，远低于 30% 门槛。选项 B（独立 `airush_ingest` 角色 + 基表直授权）不启用——它要多一个角色和一套授权，为 6% 不值得，而基表零授权这道锁是等效隔离形态的第一道锁。基准固化为 `TestViewWriteOverheadWithinBudget`，回归即失败 |
| 2026-08-15 | **T14 由 T7 覆盖，不另立用例**：`TestCollectedIsolation/T7` 本就先 `compress_chunk` 再断言隔离，且断言"存在已压缩 chunk"（否则该用例等于没验压缩）。T14 的原始表述"compress_chunk 后 T7 仍成立"与之逐字重合，另写一条只是复制 |
| 2026-08-15 | **采集侧修掉一处租户上下文漏传（实现缺陷，非 spec 变更）**：`collector.collectMetricsDirect` / `collectSnapshotDirect` 建好了 `tctx` 却只喂给 probe，落点调用传的仍是无租户的 `ctx`。spec-1.3/1.4 时期落点是内存 `BufferSink`（根本不看租户），故单测与 CI 一路绿；spec-1.5 换成 tsstore 后立刻 fail-closed 成 `AR_TENANT_CONTEXT_MISSING`，采集心跳全灭。已修，并在 collector 集成用例里加 `tenantGuardSink`——每次落点调用都断言携带租户上下文，这类"内存实现掩盖真实约束"的洞不再需要接真库才发现 |
| 2026-08-15 | **覆盖率补齐时暴露一处 trace_id 断链（顺手修）**：svcapi 的服务间认证在进入 `apierror.Middleware` **之前**就拒绝请求，直接读 `X-Trace-Id` 头，上游没带就回一个空 trace_id——恰恰是最需要能追的那条路径。`apierror` 导出 `TraceIDFrom`（无上游则自造，与 Middleware 同一套），认证出口改用它，spec-0.8 §2.2 的"trace_id 必达"在每条错误路径上都成立。补测后 console 覆盖率 78.4% → **83.3%**（svcapi/ingest.go 从 0% 到 100%，httpapi/collected.go 39% → 94%） |
| 2026-08-15 | **Code review：`TopEntities` / `/top-entities` 移出本 spec，改为显式 501，T18 随之移交 spec-1.11**。首版对累计计数器直接 `sum(value)`——慢查询统计每 5 分钟采一次，1 小时窗口 12 个样本，`Total` = 12 × 自实例启动以来的累计值，排名按"生命周期累计"排：上个月很重、今天没跑的 SQL 永远第一。T18 用例每实体只有 1 个样本，断言过了，属"绿但错"。**根因是 spec 层盲区**：§8 Q1 ★ 定"存累计原值、查询侧用 `counter_agg` 差分"，但 `counter_agg` 在 TimescaleDB Toolkit 里，本 spec 选的 `timescale/timescaledb` 镜像**不含** Toolkit（在 `timescaledb-ha` 里）；且目录没有任何字段区分 counter/gauge（`db.transactions.commit_total` 是计数器、`db.connections.active` 是 gauge），读路径无从判断。**算对它是 spec-1.11（慢查询分析 skill）的活，不属于落库层**（user 2026-08-15 定：聚焦 agent 框架，skill 具体功能不展开），故按规则 6"要么完整实现要么显式拒绝"移出。**留给 spec-1.11 的账**：① `CatalogEntry` 加 `Kind: counter/gauge`；② 计数器窗口增量 = Σ max(v−prev, 0)（重启回绕按新值计），gauge 用 avg/last；③ 排名也要按窗口起点选层（首版永远查 raw 层，>14 天窗口静默截断），聚合层需 `first_value` 列（改 0004 或补 0005）；④ 若要用 Toolkit 走 `counter_agg`，须按规则 5 硬门槛 #4 单独批镜像换 `timescaledb-ha`。`SeriesRange` 保留：它返回原始桶聚合，对计数器画图由消费方按 Kind 决定 rate 或原值（spec-1.13 消费时一并处理）。存储层不动——慢查询统计照常落 `tsdb.series` + `collected.entities`（T2 仍验实体字典与 5 条 series） |
| 2026-08-15 | **Code review：Connector 通道上报补「数据源归属」校验（fail-closed，新错误码 `AR_COLLECT_DATASOURCE_MISMATCH`）**。原实现里租户边界成立（tenant 来自 mTLS 证书 SAN，越权写被 `check_option` 拦），但 `batch.DatasourceID` 是连接器**自报**的：gateway 不核对它是否等于触发指令的数据源，console 也不核对该数据源的 `connector_id`——被攻破的连接器可以在**同租户内**往别的数据源（含 direct 数据源）灌假数据，污染日后 agent 诊断依据，且"数据是对的、只是从哪来的不对"在租户视角最难察觉。修法：内部上报请求（`/internal/v1/collected/*`）增 `connector_id`（gateway 从会话取，与 tenant 同信任基础），console 在租户事务里查 `datasources` 表：`connect_mode='connector' AND connector_id = 上报连接器`，否则 403；数据源查无 404；无归属校验器时 501（没有这道防线就收数据 = 放弃它）。**由数据库回答归属**（RLS 视图内查表），不是 gateway 内存比对。内部 API 未 shipped，不构成契约变更；错误码是本 spec 第 4 个新码，超出 §7 DoD 所列 3 个，各有触发用例（单测替身 + 真库集成） |
| 2026-08-15 | **观测性 label 维持 spec-0.9 §2.2 现有白名单，不为本 spec 扩充**：写入/查询指标只用 `status` + `code`；`kind`（metrics/slowlog/…）与 `layer`（raw/5m/1h）虽是低基数维度，但白名单扩充按 spec-0.9 §2.2 要动那份已 frozen 的 spec，为一个锦上添花的维度不值得。两者进结构化日志，排障照样可查。另在 `tsstore.New` 启动时读 `timescaledb_information.jobs` 把实际生效的压缩/保留/连续聚合策略打进日志——策略由迁移创建、由后台作业执行，进程内不可见，某次迁移漏掉 `add_retention_policy` 会让磁盘安静地涨到爆，这是最便宜的一道核对 |
