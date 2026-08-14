#!/bin/zsh
# 通用 series 表 vs 每类一张强类型表：行数膨胀后压缩率与查询代价还剩多少差。
#
# 起因（2026-08-14 user 提问）：表结构会不会随 skill 增长而无限加表。泛化成
# 「实体 + 度量 + 时间」一张表可以让"加采集能力 = 加目录条目"，代价是一条慢查询
# 从 1 行变 5 行。这里测代价是否可接受。
#
# 口径与 probe-slowlog-layout.sh 一致：5 数据源 × 50 SQL × 7 天 × 288 次/天。
set -eu
export PATH="/usr/local/bin:/opt/homebrew/bin:$PATH"

NAME=airush-series-probe
IMAGE=${TS_IMAGE:-timescale/timescaledb:latest-pg16}

docker rm -f "$NAME" >/dev/null 2>&1 || true
docker run -d --name "$NAME" -e POSTGRES_PASSWORD=probe -p 5458:5432 "$IMAGE" >/dev/null
trap 'docker rm -f "$NAME" >/dev/null 2>&1 || true' EXIT

until docker exec "$NAME" pg_isready -U postgres >/dev/null 2>&1; do sleep 2; done
sleep 3

docker exec -i "$NAME" psql -U postgres -q -v ON_ERROR_STOP=1 <<'SQL'
CREATE EXTENSION IF NOT EXISTS timescaledb;

-- 布局 A：强类型专表（现方案），一行携 5 个度量
CREATE TABLE typed_slowlog (
    tenant_id uuid NOT NULL, datasource_id uuid NOT NULL,
    query_id text NOT NULL, text text NOT NULL,
    calls bigint NOT NULL, total_ms float8 NOT NULL, mean_ms float8 NOT NULL,
    max_ms float8 NOT NULL, rows bigint NOT NULL, at timestamptz NOT NULL);
SELECT create_hypertable('typed_slowlog','at',chunk_time_interval=>interval '1 day');
ALTER TABLE typed_slowlog SET (timescaledb.compress,
    timescaledb.compress_segmentby='tenant_id, datasource_id, query_id',
    timescaledb.compress_orderby='at DESC');

-- 布局 B：通用 series，一个度量一行（同一采集变 5 行）
CREATE TABLE series (
    tenant_id uuid NOT NULL, datasource_id uuid NOT NULL,
    series_name text NOT NULL, entity_id text NOT NULL DEFAULT '',
    entity_label text NOT NULL DEFAULT '',
    value float8 NOT NULL, at timestamptz NOT NULL);
SELECT create_hypertable('series','at',chunk_time_interval=>interval '1 day');
ALTER TABLE series SET (timescaledb.compress,
    timescaledb.compress_segmentby='tenant_id, datasource_id, series_name, entity_id',
    timescaledb.compress_orderby='at DESC');

CREATE TABLE gen AS
SELECT '11111111-1111-1111-1111-111111111111'::uuid AS tenant_id,
       ('22222222-2222-2222-2222-22222222220' || ds)::uuid AS datasource_id,
       'q' || lpad(q::text,4,'0') AS query_id,
       'SELECT o.id, o.status, c.name, sum(i.qty * i.price) FROM orders o JOIN customers c ON c.id = o.customer_id JOIN order_items i ON i.order_id = o.id WHERE o.created_at >= $1 AND o.created_at < $2 AND o.status = ANY($3) AND c.region_id = $4 GROUP BY o.id, o.status, c.name HAVING sum(i.qty * i.price) > $5 ORDER BY 4 DESC LIMIT $6 -- variant ' || q AS text,
       (1000 + q*7 + t)::bigint AS calls, (12345.6 + q*3.3 + t)::float8 AS total_ms,
       (12.3 + q*0.1)::float8 AS mean_ms, (98.7 + q*1.1)::float8 AS max_ms,
       (500 + q*3)::bigint AS rows,
       (timestamptz '2026-08-01 00:00:00+00' + (t || ' minutes')::interval * 5) AS at
FROM generate_series(1,5) ds, generate_series(1,50) q, generate_series(0,7*288-1) t;

INSERT INTO typed_slowlog SELECT tenant_id,datasource_id,query_id,text,calls,total_ms,mean_ms,max_ms,rows,at FROM gen;

INSERT INTO series (tenant_id,datasource_id,series_name,entity_id,entity_label,value,at)
SELECT tenant_id,datasource_id,m.name,query_id,text,m.val,at
FROM gen, LATERAL (VALUES
    ('pg.slowlog.calls',    calls::float8),
    ('pg.slowlog.total_ms', total_ms),
    ('pg.slowlog.mean_ms',  mean_ms),
    ('pg.slowlog.max_ms',   max_ms),
    ('pg.slowlog.rows',     rows::float8)) AS m(name,val);
DROP TABLE gen;
SQL

echo "=== 行数 ==="
docker exec -i "$NAME" psql -U postgres -q <<'SQL'
SELECT 'A 强类型专表' AS layout, count(*) FROM typed_slowlog
UNION ALL SELECT 'B 通用 series', count(*) FROM series;
SQL

echo "=== 压缩前 ==="
docker exec -i "$NAME" psql -U postgres -q <<'SQL'
SELECT 'A 强类型专表' AS layout, pg_size_pretty(hypertable_size('typed_slowlog')) AS total
UNION ALL SELECT 'B 通用 series', pg_size_pretty(hypertable_size('series'));
SQL

docker exec -i "$NAME" psql -U postgres -q -v ON_ERROR_STOP=1 >/dev/null <<'SQL'
SELECT compress_chunk(c) FROM show_chunks('typed_slowlog') c;
SELECT compress_chunk(c) FROM show_chunks('series') c;
SQL

echo "=== 压缩后 ==="
docker exec -i "$NAME" psql -U postgres -q <<'SQL'
SELECT 'A 强类型专表' AS layout, pg_size_pretty(hypertable_size('typed_slowlog')) AS total
UNION ALL SELECT 'B 通用 series', pg_size_pretty(hypertable_size('series'));
SQL

echo "=== 查询 1：Top10 慢 SQL（按累计耗时）——最常用的那个 ==="
docker exec -i "$NAME" psql -U postgres -q <<'SQL'
\timing on
\o /dev/null
\set qa 'SELECT query_id, sum(total_ms) v FROM typed_slowlog WHERE tenant_id=''11111111-1111-1111-1111-111111111111'' AND datasource_id=''22222222-2222-2222-2222-222222222203'' AND at >= timestamptz ''2026-08-07 00:00:00+00'' GROUP BY 1 ORDER BY 2 DESC LIMIT 10'
\set qb 'SELECT entity_id, sum(value) v FROM series WHERE tenant_id=''11111111-1111-1111-1111-111111111111'' AND datasource_id=''22222222-2222-2222-2222-222222222203'' AND series_name=''pg.slowlog.total_ms'' AND at >= timestamptz ''2026-08-07 00:00:00+00'' GROUP BY 1 ORDER BY 2 DESC LIMIT 10'
:qa; :qa; :qa;
:qb; :qb; :qb;
\o
SELECT 'A×3 然后 B×3（上面 6 个计时）' AS note;
SQL

echo "=== 查询 2：某条 SQL 展开全部 5 个度量（B 需 pivot） ==="
docker exec -i "$NAME" psql -U postgres -q <<'SQL'
\timing on
\o /dev/null
\set pa 'SELECT at, calls, total_ms, mean_ms, max_ms, rows FROM typed_slowlog WHERE tenant_id=''11111111-1111-1111-1111-111111111111'' AND datasource_id=''22222222-2222-2222-2222-222222222203'' AND query_id=''q0042'' AND at >= timestamptz ''2026-08-07 00:00:00+00'' ORDER BY at'
\set pb 'SELECT at, max(value) FILTER (WHERE series_name=''pg.slowlog.calls'') calls, max(value) FILTER (WHERE series_name=''pg.slowlog.total_ms'') total_ms, max(value) FILTER (WHERE series_name=''pg.slowlog.mean_ms'') mean_ms, max(value) FILTER (WHERE series_name=''pg.slowlog.max_ms'') max_ms, max(value) FILTER (WHERE series_name=''pg.slowlog.rows'') rows FROM series WHERE tenant_id=''11111111-1111-1111-1111-111111111111'' AND datasource_id=''22222222-2222-2222-2222-222222222203'' AND entity_id=''q0042'' AND at >= timestamptz ''2026-08-07 00:00:00+00'' GROUP BY at ORDER BY at'
:pa; :pa; :pa;
:pb; :pb; :pb;
\o
SELECT 'A×3 然后 B×3（上面 6 个计时）' AS note;
SQL

echo "=== 布局 C：通用 series 去掉 entity_label + 实体字典表 ==="
# B 的 2.9x 存储开销疑似全在 entity_label：文本按 (series_name, entity_id) 分段，
# 一条 SQL 有 5 个 series_name 就存 5 份。抽出去应该能把开销打回 A 的水平。
docker exec -i "$NAME" psql -U postgres -q -v ON_ERROR_STOP=1 <<'SQL'
CREATE TABLE series_c (
    tenant_id uuid NOT NULL, datasource_id uuid NOT NULL,
    series_name text NOT NULL, entity_id text NOT NULL DEFAULT '',
    value float8 NOT NULL, at timestamptz NOT NULL);
SELECT create_hypertable('series_c','at',chunk_time_interval=>interval '1 day');
ALTER TABLE series_c SET (timescaledb.compress,
    timescaledb.compress_segmentby='tenant_id, datasource_id, series_name, entity_id',
    timescaledb.compress_orderby='at DESC');

CREATE TABLE entities (
    tenant_id uuid NOT NULL, datasource_id uuid NOT NULL,
    entity_kind text NOT NULL, entity_id text NOT NULL,
    label text NOT NULL, first_seen_at timestamptz NOT NULL, last_seen_at timestamptz NOT NULL,
    PRIMARY KEY (tenant_id, datasource_id, entity_kind, entity_id));

INSERT INTO series_c SELECT tenant_id,datasource_id,series_name,entity_id,value,at FROM series;
INSERT INTO entities
SELECT tenant_id,datasource_id,'query',entity_id,min(entity_label),min(at),max(at)
FROM series GROUP BY 1,2,3,4;
SELECT compress_chunk(c) FROM show_chunks('series_c') c;
SQL

docker exec -i "$NAME" psql -U postgres -q <<'SQL'
SELECT 'A 强类型专表(文本内联)' AS layout, pg_size_pretty(hypertable_size('typed_slowlog')) AS total, '-' AS dict
UNION ALL SELECT 'B 通用 series(文本内联)', pg_size_pretty(hypertable_size('series')), '-'
UNION ALL SELECT 'C 通用 series + 实体字典',
       pg_size_pretty(hypertable_size('series_c') + pg_total_relation_size('entities')),
       pg_size_pretty(pg_total_relation_size('entities'));
SQL

echo "=== C 的两个查询（各 3 次，取热态） ==="
docker exec -i "$NAME" psql -U postgres -q <<'SQL'
\timing on
\o /dev/null
\set qc 'SELECT s.entity_id, e.label, sum(s.value) v FROM series_c s JOIN entities e ON e.tenant_id=s.tenant_id AND e.datasource_id=s.datasource_id AND e.entity_kind=''query'' AND e.entity_id=s.entity_id WHERE s.tenant_id=''11111111-1111-1111-1111-111111111111'' AND s.datasource_id=''22222222-2222-2222-2222-222222222203'' AND s.series_name=''pg.slowlog.total_ms'' AND s.at >= timestamptz ''2026-08-07 00:00:00+00'' GROUP BY 1,2 ORDER BY 3 DESC LIMIT 10'
\set pc 'SELECT at, max(value) FILTER (WHERE series_name=''pg.slowlog.calls'') calls, max(value) FILTER (WHERE series_name=''pg.slowlog.total_ms'') total_ms, max(value) FILTER (WHERE series_name=''pg.slowlog.mean_ms'') mean_ms, max(value) FILTER (WHERE series_name=''pg.slowlog.max_ms'') max_ms, max(value) FILTER (WHERE series_name=''pg.slowlog.rows'') rows FROM series_c WHERE tenant_id=''11111111-1111-1111-1111-111111111111'' AND datasource_id=''22222222-2222-2222-2222-222222222203'' AND entity_id=''q0042'' AND at >= timestamptz ''2026-08-07 00:00:00+00'' GROUP BY at ORDER BY at'
:qc; :qc; :qc;
:pc; :pc; :pc;
\o
SELECT 'Top10×3 然后 展开×3' AS note;
SQL
