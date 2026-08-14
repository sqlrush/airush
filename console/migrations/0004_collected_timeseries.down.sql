-- 0004_collected_timeseries down（纯结构回滚，spec-0.6 §2.1）
--
-- 顺序：视图 → 连续聚合 → 超表 → 普通表 → schema。
-- 连续聚合与超表的策略（压缩/保留/刷新）随对象 DROP 自动注销，无需单独 remove_*_policy。
--
-- **不 DROP EXTENSION timescaledb**：扩展是环境能力而非本迁移的 schema 产物，
-- 且可能被同库其它对象依赖。up 侧用 IF NOT EXISTS，故 up→down→up 依然幂等。

DROP VIEW IF EXISTS collected.series_1h;
DROP VIEW IF EXISTS collected.series_5m;
DROP VIEW IF EXISTS collected.series;

DROP MATERIALIZED VIEW IF EXISTS tsdb.series_1h;
DROP MATERIALIZED VIEW IF EXISTS tsdb.series_5m;
DROP TABLE IF EXISTS tsdb.series;

DROP TABLE IF EXISTS collected.snapshots;
DROP TABLE IF EXISTS collected.entities;

DROP SCHEMA IF EXISTS collected;
DROP SCHEMA IF EXISTS tsdb;
