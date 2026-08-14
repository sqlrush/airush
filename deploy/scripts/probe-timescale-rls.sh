#!/bin/zsh
# 实测 TimescaleDB hypertable 上的 RLS 行为（spec-1.5 建模前置验证）。
#
# 要验三件事，任何一条不成立都会改变 spec-1.5 的表设计：
#   ① 普通租户表模板（ENABLE+FORCE+policy）能否直接套在 hypertable 上
#   ② 跨 chunk 查询时隔离是否仍然生效
#   ③ chunk 压缩之后隔离是否仍然生效（压缩改变物理存储，是最可疑的一环）
set -eu
export PATH="/usr/local/bin:/opt/homebrew/bin:$PATH"

NAME=airush-ts-probe
IMAGE=${TS_IMAGE:-timescale/timescaledb:latest-pg16}

docker rm -f "$NAME" >/dev/null 2>&1 || true
docker run -d --name "$NAME" -e POSTGRES_PASSWORD=probe -p 5455:5432 "$IMAGE" >/dev/null
trap 'docker rm -f "$NAME" >/dev/null 2>&1 || true' EXIT

until docker exec "$NAME" pg_isready -U postgres >/dev/null 2>&1; do sleep 2; done
sleep 3

docker exec -i "$NAME" psql -U postgres -v ON_ERROR_STOP=1 <<'SQL'
CREATE EXTENSION IF NOT EXISTS timescaledb;

CREATE ROLE app_role NOLOGIN;

-- 照抄 spec-0.6 §2.2 的租户表模板
CREATE TABLE metric_samples (
    tenant_id     uuid        NOT NULL,
    datasource_id uuid        NOT NULL,
    metric_name   text        NOT NULL,
    value         double precision NOT NULL,
    at            timestamptz NOT NULL
);
SELECT create_hypertable('metric_samples', 'at', chunk_time_interval => interval '1 day');

ALTER TABLE metric_samples ENABLE ROW LEVEL SECURITY;
ALTER TABLE metric_samples FORCE ROW LEVEL SECURITY;
CREATE POLICY tenant_isolation ON metric_samples
    USING (tenant_id = NULLIF(current_setting('app.tenant_id', true), '')::uuid);
GRANT SELECT, INSERT ON metric_samples TO app_role;

-- 两个租户各写 3 天数据，确保跨多个 chunk
INSERT INTO metric_samples
SELECT '11111111-1111-1111-1111-111111111111'::uuid,
       gen_random_uuid(), 'pg.connections.active', 10,
       now() - (n || ' hours')::interval
FROM generate_series(0, 71) n;
INSERT INTO metric_samples
SELECT '22222222-2222-2222-2222-222222222222'::uuid,
       gen_random_uuid(), 'pg.connections.active', 20,
       now() - (n || ' hours')::interval
FROM generate_series(0, 71) n;

SELECT count(*) AS chunks FROM timescaledb_information.chunks
 WHERE hypertable_name = 'metric_samples';
SQL

echo "=== ① 未压缩：以 app_role + 租户1 上下文查询 ==="
docker exec -i "$NAME" psql -U postgres -v ON_ERROR_STOP=1 <<'SQL'
SET ROLE app_role;
SET app.tenant_id = '11111111-1111-1111-1111-111111111111';
SELECT count(*) AS visible_rows, count(DISTINCT tenant_id) AS visible_tenants
  FROM metric_samples;
SQL

echo "=== ② 无租户上下文（fail-closed 应为 0 行） ==="
docker exec -i "$NAME" psql -U postgres -v ON_ERROR_STOP=1 <<'SQL'
SET ROLE app_role;
SELECT count(*) AS visible_rows FROM metric_samples;
SQL

echo "=== ③ 压缩后再查（压缩改变物理存储，隔离是否仍生效） ==="
docker exec -i "$NAME" psql -U postgres -v ON_ERROR_STOP=1 <<'SQL'
ALTER TABLE metric_samples SET (
    timescaledb.compress,
    timescaledb.compress_segmentby = 'tenant_id, datasource_id, metric_name'
);
SELECT count(*) AS compressed_chunks FROM (
    SELECT compress_chunk(c, if_not_compressed => true)
      FROM show_chunks('metric_samples', older_than => interval '1 hour') c
) s;

SET ROLE app_role;
SET app.tenant_id = '11111111-1111-1111-1111-111111111111';
SELECT count(*) AS visible_rows, count(DISTINCT tenant_id) AS visible_tenants
  FROM metric_samples;
SQL

echo "=== ④ 连续聚合视图上的隔离 ==="
docker exec -i "$NAME" psql -U postgres <<'SQL'
CREATE MATERIALIZED VIEW metric_hourly
WITH (timescaledb.continuous) AS
SELECT tenant_id, datasource_id, metric_name,
       time_bucket('1 hour', at) AS bucket, avg(value) AS avg_value
  FROM metric_samples
 GROUP BY tenant_id, datasource_id, metric_name, bucket;

SELECT relrowsecurity AS view_has_rls
  FROM pg_class WHERE relname = 'metric_hourly';

SET ROLE app_role;
SET app.tenant_id = '11111111-1111-1111-1111-111111111111';
SELECT count(*) AS visible_rows, count(DISTINCT tenant_id) AS visible_tenants
  FROM metric_hourly;
SQL
