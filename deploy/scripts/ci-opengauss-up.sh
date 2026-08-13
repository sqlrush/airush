#!/usr/bin/env bash
# 在 CI 上拉起一个 openGauss 容器供采集目录的真机校验用（spec-1.4）。
#
# 为什么不用 GitHub Actions 的 services:：openGauss 初始化要几十秒且健康检查
# 需要以 omm 用户执行 gsql，写进 health-cmd 的引号嵌套极易出错；这里显式轮询更可控。
#
# 凭据是这个一次性容器自带的口令，不是平台管理的凭据——AD-4 的凭据边界针对被管
# 数据库，与 CI 内起停的测试容器无关。
set -euo pipefail

IMAGE=${OG_IMAGE:-enmotech/opengauss-lite:5.0.3}
NAME=${OG_NAME:-airush-ci-opengauss}
PORT=${OG_PORT:-5433}
PASSWORD=${OG_PASSWORD:-Ci@Verify1234}
TIMEOUT=${OG_TIMEOUT:-240}

echo "==> starting $IMAGE as $NAME on :$PORT"
docker rm -f "$NAME" >/dev/null 2>&1 || true
docker run -d --name "$NAME" \
  -e GS_PASSWORD="$PASSWORD" \
  -p "$PORT":5432 \
  "$IMAGE" >/dev/null

deadline=$((SECONDS + TIMEOUT))
until docker exec "$NAME" su - omm -c \
  "/usr/local/opengauss/bin/gsql -p 5432 -d postgres -c 'select 1'" >/dev/null 2>&1; do
  if [ "$SECONDS" -ge "$deadline" ]; then
    echo "ERROR: openGauss 未在 ${TIMEOUT}s 内就绪，最后 40 行日志：" >&2
    docker logs --tail 40 "$NAME" >&2 || true
    exit 1
  fi
  sleep 3
done
echo "==> openGauss ready after ${SECONDS}s"

# 慢查询源 dbe_perf 需 monadmin。不授的话采集会降级为 CapabilityMissing——那条路径
# 有单测覆盖，而 CI 要验的是**真正跑 dbe_perf SQL** 的那条，故这里授权，对应客户
# 实际会给监控账号的权限形态。
docker exec "$NAME" su - omm -c \
  "/usr/local/opengauss/bin/gsql -p 5432 -d postgres -c 'ALTER USER gaussdb monadmin;'" >/dev/null
echo "==> granted monadmin to gaussdb (dbe_perf readable)"

# 把接入参数交给后续步骤；集成用例未见这些变量即跳过 openGauss 分支。
if [ -n "${GITHUB_ENV:-}" ]; then
  {
    echo "AIRUSH_OPENGAUSS_HOST=127.0.0.1"
    echo "AIRUSH_OPENGAUSS_PORT=$PORT"
    echo "AIRUSH_OPENGAUSS_USER=gaussdb"
    echo "AIRUSH_OPENGAUSS_DB=postgres"
    echo "AIRUSH_OPENGAUSS_PASSWORD=$PASSWORD"
  } >>"$GITHUB_ENV"
fi
