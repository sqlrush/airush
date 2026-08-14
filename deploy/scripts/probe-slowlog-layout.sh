#!/bin/zsh
# 慢查询表布局实测：文本内联 vs 文本抽字典，压缩后各占多少。
#
# 起因：spec-1.5 起草时我用"文本重复 288 次"论证要拆字典表，但列存的
# segmentby + 字典编码本来就在做同样的去重——这个论证可能是空的。用真数据量。
#
# 造数口径贴近真实：50 条慢查询 × 288 次/天（5 分钟一采）× 7 天，
# 文本长度取典型规范化 SQL（~300 字节）而非 2KB 上限。
set -eu
export PATH="/usr/local/bin:/opt/homebrew/bin:$PATH"

NAME=airush-slowlog-probe
IMAGE=${TS_IMAGE:-timescale/timescaledb:latest-pg16}

docker rm -f "$NAME" >/dev/null 2>&1 || true
docker run -d --name "$NAME" -e POSTGRES_PASSWORD=probe -p 5457:5432 "$IMAGE" >/dev/null
trap 'docker rm -f "$NAME" >/dev/null 2>&1 || true' EXIT

until docker exec "$NAME" pg_isready -U postgres >/dev/null 2>&1; do sleep 2; done
sleep 3

docker exec -i "$NAME" psql -U postgres -q -v ON_ERROR_STOP=1 <<'SQL'
CREATE EXTENSION IF NOT EXISTS timescaledb;

-- 布局 A：文本内联单表
CREATE TABLE inline_samples (
    tenant_id     uuid NOT NULL,
    datasource_id uuid NOT NULL,
    query_id      text NOT NULL,
    text          text NOT NULL,
    calls         bigint NOT NULL,
    total_ms      double precision NOT NULL,
    mean_ms       double precision NOT NULL,
    max_ms        double precision NOT NULL,
    rows          bigint NOT NULL,
    at            timestamptz NOT NULL
);
SELECT create_hypertable('inline_samples','at',chunk_time_interval=>interval '1 day');
ALTER TABLE inline_samples SET (
    timescaledb.compress,
    timescaledb.compress_segmentby='tenant_id, datasource_id, query_id',
    timescaledb.compress_orderby='at DESC');

-- 布局 B：文本抽字典 + 纯数值时序
CREATE TABLE dict_texts (
    tenant_id uuid NOT NULL, datasource_id uuid NOT NULL, query_id text NOT NULL,
    text text NOT NULL, first_seen_at timestamptz NOT NULL, last_seen_at timestamptz NOT NULL,
    PRIMARY KEY (tenant_id, datasource_id, query_id));
CREATE TABLE dict_samples (
    tenant_id     uuid NOT NULL,
    datasource_id uuid NOT NULL,
    query_id      text NOT NULL,
    calls         bigint NOT NULL,
    total_ms      double precision NOT NULL,
    mean_ms       double precision NOT NULL,
    max_ms        double precision NOT NULL,
    rows          bigint NOT NULL,
    at            timestamptz NOT NULL
);
SELECT create_hypertable('dict_samples','at',chunk_time_interval=>interval '1 day');
ALTER TABLE dict_samples SET (
    timescaledb.compress,
    timescaledb.compress_segmentby='tenant_id, datasource_id, query_id',
    timescaledb.compress_orderby='at DESC');

-- 造数：5 个数据源 × 50 条 SQL × 7 天 × 288 次/天 = 504,000 行
CREATE TABLE gen AS
SELECT '11111111-1111-1111-1111-111111111111'::uuid AS tenant_id,
       ('22222222-2222-2222-2222-22222222220' || ds)::uuid AS datasource_id,
       'q' || lpad(q::text, 4, '0') AS query_id,
       -- 典型规范化 SQL，~300 字节，每条 SQL 文本固定
       'SELECT o.id, o.status, c.name, sum(i.qty * i.price) FROM orders o JOIN customers c ON c.id = o.customer_id JOIN order_items i ON i.order_id = o.id WHERE o.created_at >= $1 AND o.created_at < $2 AND o.status = ANY($3) AND c.region_id = $4 GROUP BY o.id, o.status, c.name HAVING sum(i.qty * i.price) > $5 ORDER BY 4 DESC LIMIT $6 -- variant ' || q AS text,
       (1000 + q * 7 + t)::bigint AS calls,
       (12345.6 + q * 3.3 + t)::float8 AS total_ms,
       (12.3 + q * 0.1)::float8 AS mean_ms,
       (98.7 + q * 1.1)::float8 AS max_ms,
       (500 + q * 3)::bigint AS rows,
       (timestamptz '2026-08-01 00:00:00+00' + (t || ' minutes')::interval * 5) AS at
FROM generate_series(1,5) ds, generate_series(1,50) q, generate_series(0, 7*288-1) t;

INSERT INTO inline_samples SELECT tenant_id,datasource_id,query_id,text,calls,total_ms,mean_ms,max_ms,rows,at FROM gen;
INSERT INTO dict_samples   SELECT tenant_id,datasource_id,query_id,     calls,total_ms,mean_ms,max_ms,rows,at FROM gen;
INSERT INTO dict_texts SELECT DISTINCT ON (tenant_id,datasource_id,query_id)
       tenant_id,datasource_id,query_id,text,min(at) OVER (PARTITION BY tenant_id,datasource_id,query_id),
       max(at) OVER (PARTITION BY tenant_id,datasource_id,query_id) FROM gen;
DROP TABLE gen;
SQL

echo "=== 行数 ==="
docker exec -i "$NAME" psql -U postgres -q <<'SQL'
SELECT 'inline_samples' t, count(*) FROM inline_samples
UNION ALL SELECT 'dict_samples', count(*) FROM dict_samples
UNION ALL SELECT 'dict_texts',   count(*) FROM dict_texts;
SQL

echo "=== 压缩前 ==="
docker exec -i "$NAME" psql -U postgres -q <<'SQL'
SELECT 'A 内联单表' AS layout, pg_size_pretty(hypertable_size('inline_samples')) AS total
UNION ALL
SELECT 'B 字典拆分', pg_size_pretty(hypertable_size('dict_samples')
                                   + pg_total_relation_size('dict_texts'));
SQL

docker exec -i "$NAME" psql -U postgres -q -v ON_ERROR_STOP=1 <<'SQL' >/dev/null
SELECT compress_chunk(c) FROM show_chunks('inline_samples') c;
SELECT compress_chunk(c) FROM show_chunks('dict_samples') c;
SQL

echo "=== 压缩后 ==="
docker exec -i "$NAME" psql -U postgres -q <<'SQL'
SELECT 'A 内联单表' AS layout,
       pg_size_pretty(hypertable_size('inline_samples')) AS total,
       '-' AS texts
UNION ALL
SELECT 'B 字典拆分',
       pg_size_pretty(hypertable_size('dict_samples') + pg_total_relation_size('dict_texts')),
       pg_size_pretty(pg_total_relation_size('dict_texts'));
SQL

echo "=== 查询：某数据源最近 24h 的 Top10（两种布局各跑一次） ==="
docker exec -i "$NAME" psql -U postgres -q <<'SQL'
\timing on
SELECT query_id, sum(calls) FROM inline_samples
 WHERE tenant_id='11111111-1111-1111-1111-111111111111'
   AND datasource_id='22222222-2222-2222-2222-222222222203'
   AND at >= timestamptz '2026-08-07 00:00:00+00'
 GROUP BY 1 ORDER BY 2 DESC LIMIT 10;
SELECT s.query_id, sum(s.calls) FROM dict_samples s
 WHERE s.tenant_id='11111111-1111-1111-1111-111111111111'
   AND s.datasource_id='22222222-2222-2222-2222-222222222203'
   AND s.at >= timestamptz '2026-08-07 00:00:00+00'
 GROUP BY 1 ORDER BY 2 DESC LIMIT 10;
SQL

echo "=== 复测：交换顺序 + 各跑 3 次，排除冷缓存造成的假差异 ==="
docker exec -i "$NAME" psql -U postgres -q <<'SQL'
\timing on
\set q_dict 'SELECT s.query_id, sum(s.calls) FROM dict_samples s WHERE s.tenant_id=''11111111-1111-1111-1111-111111111111'' AND s.datasource_id=''22222222-2222-2222-2222-222222222203'' AND s.at >= timestamptz ''2026-08-07 00:00:00+00'' GROUP BY 1 ORDER BY 2 DESC LIMIT 10'
\set q_inline 'SELECT query_id, sum(calls) FROM inline_samples WHERE tenant_id=''11111111-1111-1111-1111-111111111111'' AND datasource_id=''22222222-2222-2222-2222-222222222203'' AND at >= timestamptz ''2026-08-07 00:00:00+00'' GROUP BY 1 ORDER BY 2 DESC LIMIT 10'
\o /dev/null
:q_dict;
:q_dict;
:q_dict;
:q_inline;
:q_inline;
:q_inline;
\o
SELECT 'done' AS reversed_order_3x_each;
SQL
