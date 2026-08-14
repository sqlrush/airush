#!/bin/zsh
# 三种布局的干净重测（前两版脚本两次跑出的 B 体积互相打架，且"抽字典能省多少"
# 的假设被证伪，需要可信数字）。
#
# 修正点：
#   ① 三张表在同一容器里同样流程建 → 压缩 → VACUUM ANALYZE → 才量体积；
#   ② 每个体积量两次，确认稳定；
#   ③ C 的 Top10 查询改成"先聚合取前 10 再 join 字典"——前版把 14400 行先 join
#      再聚合，量到的是我 SQL 写得烂，不是布局的代价。
set -eu
export PATH="/usr/local/bin:/opt/homebrew/bin:$PATH"

NAME=airush-series-final
IMAGE=${TS_IMAGE:-timescale/timescaledb:latest-pg16}

docker rm -f "$NAME" >/dev/null 2>&1 || true
docker run -d --name "$NAME" -e POSTGRES_PASSWORD=probe -p 5459:5432 "$IMAGE" >/dev/null
trap 'docker rm -f "$NAME" >/dev/null 2>&1 || true' EXIT
until docker exec "$NAME" pg_isready -U postgres >/dev/null 2>&1; do sleep 2; done
sleep 3

docker exec -i "$NAME" psql -U postgres -q -v ON_ERROR_STOP=1 <<'SQL'
CREATE EXTENSION IF NOT EXISTS timescaledb;

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

-- A：强类型专表，文本内联
CREATE TABLE lay_a (tenant_id uuid NOT NULL, datasource_id uuid NOT NULL,
    query_id text NOT NULL, text text NOT NULL, calls bigint NOT NULL,
    total_ms float8 NOT NULL, mean_ms float8 NOT NULL, max_ms float8 NOT NULL,
    rows bigint NOT NULL, at timestamptz NOT NULL);
SELECT create_hypertable('lay_a','at',chunk_time_interval=>interval '1 day');
ALTER TABLE lay_a SET (timescaledb.compress,
    timescaledb.compress_segmentby='tenant_id, datasource_id, query_id',
    timescaledb.compress_orderby='at DESC');

-- B：通用 series，文本内联
CREATE TABLE lay_b (tenant_id uuid NOT NULL, datasource_id uuid NOT NULL,
    series_name text NOT NULL, entity_id text NOT NULL, entity_label text NOT NULL,
    value float8 NOT NULL, at timestamptz NOT NULL);
SELECT create_hypertable('lay_b','at',chunk_time_interval=>interval '1 day');
ALTER TABLE lay_b SET (timescaledb.compress,
    timescaledb.compress_segmentby='tenant_id, datasource_id, series_name, entity_id',
    timescaledb.compress_orderby='at DESC');

-- C：通用 series 无文本 + 实体字典
CREATE TABLE lay_c (tenant_id uuid NOT NULL, datasource_id uuid NOT NULL,
    series_name text NOT NULL, entity_id text NOT NULL,
    value float8 NOT NULL, at timestamptz NOT NULL);
SELECT create_hypertable('lay_c','at',chunk_time_interval=>interval '1 day');
ALTER TABLE lay_c SET (timescaledb.compress,
    timescaledb.compress_segmentby='tenant_id, datasource_id, series_name, entity_id',
    timescaledb.compress_orderby='at DESC');
CREATE TABLE lay_c_entities (tenant_id uuid NOT NULL, datasource_id uuid NOT NULL,
    entity_kind text NOT NULL, entity_id text NOT NULL, label text NOT NULL,
    first_seen_at timestamptz NOT NULL, last_seen_at timestamptz NOT NULL,
    PRIMARY KEY (tenant_id,datasource_id,entity_kind,entity_id));

INSERT INTO lay_a SELECT tenant_id,datasource_id,query_id,text,calls,total_ms,mean_ms,max_ms,rows,at FROM gen;
INSERT INTO lay_b SELECT tenant_id,datasource_id,m.name,query_id,text,m.val,at FROM gen,
  LATERAL (VALUES ('pg.slowlog.calls',calls::float8),('pg.slowlog.total_ms',total_ms),
                  ('pg.slowlog.mean_ms',mean_ms),('pg.slowlog.max_ms',max_ms),
                  ('pg.slowlog.rows',rows::float8)) AS m(name,val);
INSERT INTO lay_c SELECT tenant_id,datasource_id,m.name,query_id,m.val,at FROM gen,
  LATERAL (VALUES ('pg.slowlog.calls',calls::float8),('pg.slowlog.total_ms',total_ms),
                  ('pg.slowlog.mean_ms',mean_ms),('pg.slowlog.max_ms',max_ms),
                  ('pg.slowlog.rows',rows::float8)) AS m(name,val);
INSERT INTO lay_c_entities SELECT tenant_id,datasource_id,'query',query_id,min(text),min(at),max(at)
  FROM gen GROUP BY 1,2,3,4;
DROP TABLE gen;

SELECT compress_chunk(c) FROM show_chunks('lay_a') c;
SELECT compress_chunk(c) FROM show_chunks('lay_b') c;
SELECT compress_chunk(c) FROM show_chunks('lay_c') c;
VACUUM ANALYZE;
SQL

for pass in 1 2; do
  echo "=== 体积（第 $pass 次量，两次一致才可信）==="
  docker exec -i "$NAME" psql -U postgres -q <<'SQL'
SELECT 'A 强类型专表' AS layout, count(*) AS rows,
       pg_size_pretty(hypertable_size('lay_a')) AS compressed FROM lay_a
UNION ALL SELECT 'B 通用+文本内联', count(*), pg_size_pretty(hypertable_size('lay_b')) FROM lay_b
UNION ALL SELECT 'C 通用+实体字典', count(*),
       pg_size_pretty(hypertable_size('lay_c') + pg_total_relation_size('lay_c_entities')) FROM lay_c;
SQL
done

echo "=== 查询 1：Top10 慢 SQL 按累计耗时（各 4 次，看热态末两次）==="
docker exec -i "$NAME" psql -U postgres -q <<'SQL'
\timing on
\o /dev/null
\set a 'SELECT query_id, sum(total_ms) v FROM lay_a WHERE tenant_id=''11111111-1111-1111-1111-111111111111'' AND datasource_id=''22222222-2222-2222-2222-222222222203'' AND at>=timestamptz ''2026-08-07 00:00:00+00'' GROUP BY 1 ORDER BY 2 DESC LIMIT 10'
\set b 'SELECT entity_id, sum(value) v FROM lay_b WHERE tenant_id=''11111111-1111-1111-1111-111111111111'' AND datasource_id=''22222222-2222-2222-2222-222222222203'' AND series_name=''pg.slowlog.total_ms'' AND at>=timestamptz ''2026-08-07 00:00:00+00'' GROUP BY 1 ORDER BY 2 DESC LIMIT 10'
\set c 'WITH top AS (SELECT entity_id, sum(value) v FROM lay_c WHERE tenant_id=''11111111-1111-1111-1111-111111111111'' AND datasource_id=''22222222-2222-2222-2222-222222222203'' AND series_name=''pg.slowlog.total_ms'' AND at>=timestamptz ''2026-08-07 00:00:00+00'' GROUP BY 1 ORDER BY 2 DESC LIMIT 10) SELECT t.entity_id, e.label, t.v FROM top t JOIN lay_c_entities e ON e.tenant_id=''11111111-1111-1111-1111-111111111111'' AND e.datasource_id=''22222222-2222-2222-2222-222222222203'' AND e.entity_kind=''query'' AND e.entity_id=t.entity_id'
\echo '--- A ---'
:a; :a; :a; :a;
\echo '--- B ---'
:b; :b; :b; :b;
\echo '--- C ---'
:c; :c; :c; :c;
\o
SELECT 'q1 done' AS note;
SQL

echo "=== 查询 2：单条 SQL 展开 5 个度量（B/C 需 pivot；各 4 次）==="
docker exec -i "$NAME" psql -U postgres -q <<'SQL'
\timing on
\o /dev/null
\set a 'SELECT at, calls, total_ms, mean_ms, max_ms, rows FROM lay_a WHERE tenant_id=''11111111-1111-1111-1111-111111111111'' AND datasource_id=''22222222-2222-2222-2222-222222222203'' AND query_id=''q0042'' AND at>=timestamptz ''2026-08-07 00:00:00+00'' ORDER BY at'
\set c 'SELECT at, max(value) FILTER (WHERE series_name=''pg.slowlog.calls'') calls, max(value) FILTER (WHERE series_name=''pg.slowlog.total_ms'') total_ms, max(value) FILTER (WHERE series_name=''pg.slowlog.mean_ms'') mean_ms, max(value) FILTER (WHERE series_name=''pg.slowlog.max_ms'') max_ms, max(value) FILTER (WHERE series_name=''pg.slowlog.rows'') rows FROM lay_c WHERE tenant_id=''11111111-1111-1111-1111-111111111111'' AND datasource_id=''22222222-2222-2222-2222-222222222203'' AND entity_id=''q0042'' AND at>=timestamptz ''2026-08-07 00:00:00+00'' GROUP BY at ORDER BY at'
\echo '--- A ---'
:a; :a; :a; :a;
\echo '--- C ---'
:c; :c; :c; :c;
\o
SELECT 'q2 done' AS note;
SQL
