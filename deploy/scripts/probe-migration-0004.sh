#!/bin/zsh
# 0004 迁移的快速验证环（spec-1.5 D1 开发期用，非 CI 资产）。
#
# 为什么单独一个脚本而不是直接跑 Go 集成测试：迁移 SQL 的报错要快速迭代，
# 起 Go 测试链路每轮多花一分钟。这里用 psql -c 把整个文件当**一次** Exec 发出，
# 与 golang-migrate 的执行形态一致——逐句发（psql -f）验不出"事务块里不能建
# 连续聚合"这类问题（2026-08-14 踩过）。
set -eu
export PATH="/usr/local/bin:/opt/homebrew/bin:$PATH"

NAME=airush-mig0004-probe
IMAGE=${TS_IMAGE:-timescale/timescaledb:2.29.1-pg16}
REPO=/Users/sqlrush/airush

docker rm -f "$NAME" >/dev/null 2>&1 || true
docker run -d --name "$NAME" -e POSTGRES_PASSWORD=probe -e POSTGRES_DB=airush "$IMAGE" >/dev/null
trap 'docker rm -f "$NAME" >/dev/null 2>&1 || true' EXIT
until docker exec "$NAME" pg_isready -U postgres -d airush >/dev/null 2>&1; do sleep 2; done
sleep 2

# 前置迁移逐句应用即可（它们不含事务块受限语句）。
for f in 0001_rls_foundation 0002_domain_model 0003_connector_enrollment; do
  docker exec -i "$NAME" psql -U postgres -d airush -q -v ON_ERROR_STOP=1 \
    < "$REPO/console/migrations/$f.up.sql" || { echo "FAIL: $f" >&2; exit 1; }
done
echo "==> 0001-0003 applied"

run_as_one_exec() {  # $1 = 迁移文件路径
  docker exec -i "$NAME" psql -U postgres -d airush -q -v ON_ERROR_STOP=1 \
    -c "$(cat "$1")"
}

echo "==> 0004 up（单次 Exec，等价 golang-migrate）"
run_as_one_exec "$REPO/console/migrations/0004_collected_timeseries.up.sql"
echo "==> 0004 up OK"

echo "==> 对象清点"
docker exec -i "$NAME" psql -U postgres -d airush -q <<'SQL'
SELECT hypertable_name, compression_enabled FROM timescaledb_information.hypertables
 WHERE hypertable_schema IN ('tsdb','_timescaledb_internal') ORDER BY 1;
-- materialized_only 必须为 f：为 t 时刚写入未物化的点在视图里看不见，
-- 控制台会出现"最近几分钟没数据"的假缺口（TimescaleDB 2.13+ 默认是 t）。
SELECT view_name, materialized_only FROM timescaledb_information.continuous_aggregates ORDER BY 1;
SELECT proc_name, config->>'hypertable_id' FROM timescaledb_information.jobs
 WHERE proc_name <> 'policy_telemetry' ORDER BY 1;
SELECT table_name, table_type FROM information_schema.tables
 WHERE table_schema = 'collected' ORDER BY 1;
SQL

echo "==> 0004 down"
run_as_one_exec "$REPO/console/migrations/0004_collected_timeseries.down.sql"
echo "==> 0004 down OK"

echo "==> 0004 re-up（幂等性）"
run_as_one_exec "$REPO/console/migrations/0004_collected_timeseries.up.sql"
echo "==> 0004 re-up OK —— up→down→up 通过"
