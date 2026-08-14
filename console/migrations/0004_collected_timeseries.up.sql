-- 0004_collected_timeseries（spec-1.5 D1，user approve 2026-08-14）
--
-- 采集数据落库。两个 schema、三张表、两层连续聚合、隔离视图。
--
-- 【表数收敛承诺】采集侧表数就是这三张，永久固定。往后新增采集能力（等待事件、
-- 索引使用、表膨胀……）与新增引擎（MySQL/达梦）一律只加 libs/metrics 里的编译期
-- 目录常量，零 DDL 零迁移。本文件的泛化设计（series_name/entity_id/kind 皆为**数据**
-- 而非列）全部服务于这条承诺。
--
-- 【AD-10 等效隔离形态·全仓唯一使用者】
-- 起因：TimescaleDB 列存压缩与 RLS 在同一张表**互斥**
-- （columnstore cannot be used on table with row security，deploy/scripts/probe-timescale-rls.sh 实测）。
-- 故指标超表不挂 RLS，改由「基表零授权 + security_barrier 视图 + check_option」承载隔离——
-- airush_app 对 tsdb schema **连 USAGE 都没有**，不是"有权限但被 policy 挡住"，是连表名都引用不到。
-- 隔离依然由数据库强制，不是应用层 WHERE 过滤。四项准入门槛见 spec-1.5 §2.5，
-- 对应集成用例 T7-T10（任一不过本迁移不可上线）。
-- 非超表的 entities/snapshots 照旧走 spec-0.6 §2.2 标准 RLS 模板。
--
-- 【命名即防线】自然名（collected.series）给视图，基表藏在 tsdb schema。
-- 凭习惯写 collected.series 拿到的就是隔离视图；想碰基表得主动写 tsdb. 前缀，
-- 写了也会被拒。安全的那条是默认的那条。

CREATE EXTENSION IF NOT EXISTS timescaledb;

-- ============================================================
-- schema 与授权
-- ============================================================

-- 基表专用。REVOKE 后**不**授 USAGE 给 airush_app —— 等效隔离的第一道锁。
CREATE SCHEMA tsdb;
REVOKE ALL ON SCHEMA tsdb FROM PUBLIC;

-- 采集数据对外可见面。独立 schema 让"这是采集来的客户库数据、不是平台自身数据"
-- 写在名字上——控制面 PG 自己也是 PG，不区分极易误读。
CREATE SCHEMA collected;
GRANT USAGE ON SCHEMA collected TO airush_app;

-- ============================================================
-- 表 1：tsdb.series —— 读数流水（超表 + 列存压缩）
-- ============================================================
--
-- 指标、慢查询统计、以及未来所有「实体 + 数值 + 时间」形态的采集产物都进这张表。
-- 慢查询的 5 个度量拆成 5 条 series（entity_id = 该 SQL 的实体 ID）。
--
-- 故意没有的列（每条都是取舍不是遗漏，理由见 spec-1.5 §2.2）：
--   id 主键 —— 时序表无单行寻址需求，每行多 16 字节且压缩不掉；
--   unit —— 编译期常量，随 series_name 唯一确定；
--   engine —— 数据源属性，读时 join datasources，存了就是每行重复的死数据；
--   entity_label —— 归 collected.entities。实测内联使压缩后体积 5384→8976 kB（+67%），
--                   因为一条 SQL 有 5 个 series_name 就要存 5 份；
--   labels jsonb —— 列存里 segment 不了压缩率断崖，且是 AD-3 的任意键值口子。
CREATE TABLE tsdb.series (
    tenant_id     uuid             NOT NULL,
    datasource_id uuid             NOT NULL,
    series_name   text             NOT NULL,
    entity_id     text             NOT NULL DEFAULT '',
    value         double precision NOT NULL,
    at            timestamptz      NOT NULL
);

COMMENT ON TABLE tsdb.series IS
    '读数流水基表（spec-1.5）。airush_app 无权访问；经 collected.series 视图隔离读写。';

-- 无外键指向 datasources：超表 + 压缩下的 FK 级联删除代价过高，且保留期（14 天）
-- 天然清理孤儿行。数据源删除后其读数最多残留一个保留周期——已在 spec-1.5 §3.9 登记，
-- entities/snapshots 走 FK 级联，两者行为差异是有意的。
SELECT create_hypertable('tsdb.series', by_range('at', INTERVAL '1 day'));

ALTER TABLE tsdb.series SET (
    timescaledb.compress,
    timescaledb.compress_segmentby = 'tenant_id, datasource_id, series_name, entity_id',
    timescaledb.compress_orderby   = 'at DESC'
);

-- 压缩延迟须 >> 采集迟到窗口（分钟级），否则迟到数据落进已压缩 chunk 会显著退化。
SELECT add_compression_policy('tsdb.series', INTERVAL '7 days');
SELECT add_retention_policy  ('tsdb.series', INTERVAL '14 days');

CREATE INDEX series_lookup_idx
    ON tsdb.series (tenant_id, datasource_id, series_name, at DESC);

-- ============================================================
-- 连续聚合两层：5m ← 原始点，1h ← 5m（分层卷，不重扫原始点）
-- ============================================================
--
-- 为什么两层：60s 采集下，"最近 24 小时"要扫 1440 个原始点/指标，5 分钟层降到 288；
-- "最近一年"必须走 1 小时层（否则 52 万点）。1 小时粒度看不出短时抖动，
-- 而抖动正是 DBA 要看的。
--
-- WITH NO DATA 是必需的，不是优化：CREATE MATERIALIZED VIEW ... WITH DATA 不能在
-- 事务块里执行，而 golang-migrate 整文件单次 Exec = 一个隐式事务块（2026-08-14 实测，
-- 报错 "cannot run inside a transaction block" 且整批回滚）。空壳由刷新策略回填。
--
-- materialized_only = false 显式打开 real-time aggregation：TimescaleDB 2.13+ 默认为
-- true，那样刚写入但未物化的点在视图里看不见，控制台会出现"最近几分钟没数据"的假缺口。
CREATE MATERIALIZED VIEW tsdb.series_5m
    WITH (timescaledb.continuous, timescaledb.materialized_only = false) AS
SELECT tenant_id, datasource_id, series_name, entity_id,
       time_bucket(INTERVAL '5 minutes', at) AS bucket,
       avg(value)   AS avg_value,
       min(value)   AS min_value,
       max(value)   AS max_value,
       last(value, at) AS last_value,
       count(*)     AS sample_count
FROM tsdb.series
GROUP BY tenant_id, datasource_id, series_name, entity_id, bucket
WITH NO DATA;

CREATE MATERIALIZED VIEW tsdb.series_1h
    WITH (timescaledb.continuous, timescaledb.materialized_only = false) AS
SELECT tenant_id, datasource_id, series_name, entity_id,
       time_bucket(INTERVAL '1 hour', bucket) AS bucket,
       -- 二次聚合按样本数加权，避免"平均的平均"在样本数不均时失真。
       sum(avg_value * sample_count) / NULLIF(sum(sample_count), 0) AS avg_value,
       min(min_value)  AS min_value,
       max(max_value)  AS max_value,
       last(last_value, bucket) AS last_value,
       sum(sample_count) AS sample_count
FROM tsdb.series_5m
-- GROUP BY 写完整表达式而非输出别名 bucket：源列也叫 bucket，PG 会优先解析为源列，
-- 那样就变成"按 5 分钟桶分组"——静默错误，聚合结果看着正常但粒度是错的。
GROUP BY tenant_id, datasource_id, series_name, entity_id,
         time_bucket(INTERVAL '1 hour', bucket)
WITH NO DATA;

SELECT add_continuous_aggregate_policy('tsdb.series_5m',
    start_offset      => INTERVAL '3 hours',
    end_offset        => INTERVAL '10 minutes',
    schedule_interval => INTERVAL '5 minutes');

SELECT add_continuous_aggregate_policy('tsdb.series_1h',
    start_offset      => INTERVAL '1 day',
    end_offset        => INTERVAL '1 hour',
    schedule_interval => INTERVAL '30 minutes');

SELECT add_retention_policy('tsdb.series_5m', INTERVAL '90 days');
SELECT add_retention_policy('tsdb.series_1h', INTERVAL '400 days');

-- ============================================================
-- 表 2：collected.entities —— 实体字典（标准 RLS 模板）
-- ============================================================
--
-- 双重收益，不是纯存储优化：
--   ① 省 40% 存储（实测 8976 → 5384 kB）——SQL 文本不必在 5 条 series 上各存一份；
--   ② 给"实体"稳定挂载点——first_seen_at 直接回答"这条慢 SQL 是这周才冒出来的"，
--      纯时序表里要扫全历史才得出。后续的人工忽略标记、agent 分析结论也挂这里。
--
-- 不随时间增长（只随"出现过的不同实体数"增长），不需要压缩，因而不需要等效形态。
CREATE TABLE collected.entities (
    tenant_id     uuid        NOT NULL REFERENCES tenants(id),
    datasource_id uuid        NOT NULL,
    entity_kind   text        NOT NULL,
    entity_id     text        NOT NULL,
    label         text        NOT NULL,
    -- 引擎原生标识（PG queryid / openGauss unique_sql_id）。entity_id 用平台侧
    -- sha256(规范化文本) 是为了让同一条 SQL 在主备两个实例上是**同一个实体**
    -- （原生 ID 实例重启会变、跨实例不可比）；原值留此供排障对照。
    native_id     text        NOT NULL DEFAULT '',
    -- 引擎特有属性（MySQL 的 ENGINE=/字符集、达梦的自有属性）。
    -- 键必须在采集目录中预先声明，未声明者由 sink 显式拒绝——AD-3 防线，
    -- 否则这里就是任意数据的口子。
    attributes    jsonb       NOT NULL DEFAULT '{}'::jsonb,
    first_seen_at timestamptz NOT NULL,
    last_seen_at  timestamptz NOT NULL,
    PRIMARY KEY (tenant_id, datasource_id, entity_kind, entity_id),
    FOREIGN KEY (tenant_id, datasource_id)
        REFERENCES datasources(tenant_id, id) ON DELETE CASCADE
);
ALTER TABLE collected.entities ENABLE ROW LEVEL SECURITY;
ALTER TABLE collected.entities FORCE  ROW LEVEL SECURITY;
CREATE POLICY tenant_isolation ON collected.entities
    USING (tenant_id = NULLIF(current_setting('app.tenant_id', true), '')::uuid);
GRANT SELECT, INSERT, UPDATE ON collected.entities TO airush_app;

CREATE INDEX entities_recent_idx
    ON collected.entities (tenant_id, datasource_id, entity_kind, last_seen_at DESC);

-- ============================================================
-- 表 3：collected.snapshots —— 慢变状态（标准 RLS 模板）
-- ============================================================
--
-- 表结构 / 实例配置。**只在内容变化时才插新行**：哈希相同仅更新 collected_at，
-- 不同则旧行 superseded + 插新行。于是这张表天然是变更历史，而不是 24 份一模一样的快照。
--
-- kind='slowlog' 不入此表：慢查询每次采集内容必变，哈希去重完全失效，
-- 且它需要按实体做趋势，形态属读数流水，走 tsdb.series。
CREATE TABLE collected.snapshots (
    tenant_id          uuid        NOT NULL REFERENCES tenants(id),
    id                 uuid        NOT NULL DEFAULT gen_random_uuid(),
    datasource_id      uuid        NOT NULL,
    kind               text        NOT NULL CHECK (kind IN ('schema', 'config')),
    source             text        NOT NULL DEFAULT '',
    capability_missing boolean     NOT NULL DEFAULT false,
    truncated          boolean     NOT NULL DEFAULT false,
    catalog_version    int         NOT NULL,
    content_hash       text        NOT NULL,
    payload            jsonb       NOT NULL,
    collected_at       timestamptz NOT NULL,
    created_at         timestamptz NOT NULL DEFAULT now(),
    superseded_at      timestamptz,
    PRIMARY KEY (tenant_id, id),
    FOREIGN KEY (tenant_id, datasource_id)
        REFERENCES datasources(tenant_id, id) ON DELETE CASCADE
);
ALTER TABLE collected.snapshots ENABLE ROW LEVEL SECURITY;
ALTER TABLE collected.snapshots FORCE  ROW LEVEL SECURITY;
CREATE POLICY tenant_isolation ON collected.snapshots
    USING (tenant_id = NULLIF(current_setting('app.tenant_id', true), '')::uuid);
GRANT SELECT, INSERT, UPDATE ON collected.snapshots TO airush_app;

-- 每个 (数据源, kind) 至多一个当前版本
CREATE UNIQUE INDEX snapshots_current_idx
    ON collected.snapshots (tenant_id, datasource_id, kind)
    WHERE superseded_at IS NULL;
CREATE INDEX snapshots_history_idx
    ON collected.snapshots (tenant_id, datasource_id, kind, created_at DESC);

-- ============================================================
-- 隔离视图（AD-10 等效形态的承载点）
-- ============================================================
--
-- security_barrier：阻止用户提供的低成本函数在租户谓词之前求值而泄露行内容；
-- check_option = cascaded：阻止越权**写入**。后者在 probe-timescale-rls2.sh 初验时
-- 漏加过，伪造他人 tenant_id 的 INSERT 当时**没被拦住**——security_barrier 只管读不管写。
-- 这就是把"四项门槛"写成硬要求而不是一句"用视图也行"的理由。

CREATE VIEW collected.series
    WITH (security_barrier = true, check_option = cascaded) AS
SELECT tenant_id, datasource_id, series_name, entity_id, value, at
  FROM tsdb.series
 WHERE tenant_id = NULLIF(current_setting('app.tenant_id', true), '')::uuid;
GRANT SELECT, INSERT ON collected.series TO airush_app;

CREATE VIEW collected.series_5m WITH (security_barrier = true) AS
SELECT tenant_id, datasource_id, series_name, entity_id, bucket,
       avg_value, min_value, max_value, last_value, sample_count
  FROM tsdb.series_5m
 WHERE tenant_id = NULLIF(current_setting('app.tenant_id', true), '')::uuid;
GRANT SELECT ON collected.series_5m TO airush_app;

CREATE VIEW collected.series_1h WITH (security_barrier = true) AS
SELECT tenant_id, datasource_id, series_name, entity_id, bucket,
       avg_value, min_value, max_value, last_value, sample_count
  FROM tsdb.series_1h
 WHERE tenant_id = NULLIF(current_setting('app.tenant_id', true), '')::uuid;
GRANT SELECT ON collected.series_1h TO airush_app;
