#!/bin/zsh
# 续验：既然 RLS 与列存压缩互斥（probe-timescale-rls.sh 实测），验证"视图承载隔离"
# 方案能否两者兼得——
#   基表 hypertable 不挂 RLS（压缩可用），应用角色对基表**零权限**；
#   只对一个按 app.tenant_id 过滤的 security_barrier 视图授权。
# 隔离依然由数据库强制（应用拿不到基表），不是应用层 WHERE 过滤。
set -eu
export PATH="/usr/local/bin:/opt/homebrew/bin:$PATH"

NAME=airush-ts-probe2
IMAGE=${TS_IMAGE:-timescale/timescaledb:latest-pg16}

docker rm -f "$NAME" >/dev/null 2>&1 || true
docker run -d --name "$NAME" -e POSTGRES_PASSWORD=probe -p 5456:5432 "$IMAGE" >/dev/null
trap 'docker rm -f "$NAME" >/dev/null 2>&1 || true' EXIT

until docker exec "$NAME" pg_isready -U postgres >/dev/null 2>&1; do sleep 2; done
sleep 3

docker exec -i "$NAME" psql -U postgres -v ON_ERROR_STOP=1 <<'SQL'
CREATE EXTENSION IF NOT EXISTS timescaledb;
CREATE ROLE app_role NOLOGIN;
GRANT USAGE ON SCHEMA public TO app_role;

CREATE TABLE metric_samples (
    tenant_id     uuid        NOT NULL,
    datasource_id uuid        NOT NULL,
    metric_name   text        NOT NULL,
    value         double precision NOT NULL,
    at            timestamptz NOT NULL
);
SELECT create_hypertable('metric_samples', 'at', chunk_time_interval => interval '1 day');

-- 基表：不挂 RLS（压缩前提），且**不给应用角色任何权限**
ALTER TABLE metric_samples SET (
    timescaledb.compress,
    timescaledb.compress_segmentby = 'tenant_id, datasource_id, metric_name'
);

CREATE VIEW metric_samples_v WITH (security_barrier) AS
SELECT * FROM metric_samples
 WHERE tenant_id = NULLIF(current_setting('app.tenant_id', true), '')::uuid;
GRANT SELECT, INSERT ON metric_samples_v TO app_role;

INSERT INTO metric_samples
SELECT '11111111-1111-1111-1111-111111111111'::uuid, gen_random_uuid(),
       'pg.connections.active', 10, now() - (n || ' hours')::interval
FROM generate_series(0, 71) n;
INSERT INTO metric_samples
SELECT '22222222-2222-2222-2222-222222222222'::uuid, gen_random_uuid(),
       'pg.connections.active', 20, now() - (n || ' hours')::interval
FROM generate_series(0, 71) n;

SELECT count(*) AS compressed FROM (
    SELECT compress_chunk(c, if_not_compressed => true)
      FROM show_chunks('metric_samples', older_than => interval '1 hour') c
) s;
SQL

echo "=== ① 压缩已启用的前提下，视图隔离是否生效 ==="
docker exec -i "$NAME" psql -U postgres -v ON_ERROR_STOP=1 <<'SQL'
SET ROLE app_role;
SET app.tenant_id = '11111111-1111-1111-1111-111111111111';
SELECT count(*) AS visible_rows, count(DISTINCT tenant_id) AS visible_tenants
  FROM metric_samples_v;
SQL

echo "=== ② 无租户上下文（应 0 行，fail-closed） ==="
docker exec -i "$NAME" psql -U postgres -v ON_ERROR_STOP=1 <<'SQL'
SET ROLE app_role;
SELECT count(*) AS visible_rows FROM metric_samples_v;
SQL

echo "=== ③ 应用角色能否绕过视图直接读基表（必须被拒） ==="
docker exec -i "$NAME" psql -U postgres <<'SQL'
SET ROLE app_role;
SET app.tenant_id = '11111111-1111-1111-1111-111111111111';
SELECT count(*) FROM metric_samples;
SQL

echo "=== ④ 经视图写入是否落到正确租户分区 ==="
docker exec -i "$NAME" psql -U postgres <<'SQL'
SET ROLE app_role;
SET app.tenant_id = '11111111-1111-1111-1111-111111111111';
INSERT INTO metric_samples_v (tenant_id, datasource_id, metric_name, value, at)
VALUES ('11111111-1111-1111-1111-111111111111', gen_random_uuid(), 'probe.write', 1, now());
SELECT count(*) AS after_insert FROM metric_samples_v;
SQL

echo "=== ⑤ 越权写入（伪造他人 tenant_id）是否被挡 ==="
docker exec -i "$NAME" psql -U postgres <<'SQL'
SET ROLE app_role;
SET app.tenant_id = '11111111-1111-1111-1111-111111111111';
INSERT INTO metric_samples_v (tenant_id, datasource_id, metric_name, value, at)
VALUES ('22222222-2222-2222-2222-222222222222', gen_random_uuid(), 'evil', 1, now());
SQL
