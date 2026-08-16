#!/bin/zsh
# 测 LiteLLM 容器的启动内存峰值与 entrypoint 形态（k8s 上 exit 137 无日志的排障）。
set -u
export PATH="/opt/homebrew/bin:/usr/local/bin:$PATH"
cd /Users/sqlrush/airush/deploy/scripts/probe-litellm
IMG=ghcr.io/berriai/litellm@sha256:154e23bb5f31b1f10e16392a8ef299bd2cde08de3a64a6849002cfcc25ce3c63
echo "== entrypoint/cmd =="
docker inspect "$IMG" --format 'Entrypoint={{json .Config.Entrypoint}} Cmd={{json .Config.Cmd}} User={{.Config.User}}'
LIMIT=${1:-1g}
echo "== 以 --memory=$LIMIT 启动，观察 60s =="
docker rm -f litellm-mem >/dev/null 2>&1
docker run -d --name litellm-mem --memory="$LIMIT" -p 14001:4000 -e LITELLM_MASTER_KEY=probe-master-key-not-a-secret \
  -e DISABLE_ADMIN_UI=True -v "$PWD/config2.yaml:/app/config.yaml:ro" "$IMG" --config /app/config.yaml --port 4000 >/dev/null
for i in $(seq 1 12); do
  sleep 5
  st=$(docker inspect litellm-mem --format '{{.State.Status}} exit={{.State.ExitCode}} oom={{.State.OOMKilled}}')
  mem=$(docker stats --no-stream --format '{{.MemUsage}}' litellm-mem 2>/dev/null)
  live=$(curl -s -o /dev/null -w '%{http_code}' http://localhost:14001/health/liveliness 2>/dev/null)
  echo "t=$((i*5))s $st mem=$mem liveliness=$live"
done
echo "== 容器内进程树 =="
docker exec litellm-mem sh -c 'ps -eo pid,ppid,rss,cmd 2>/dev/null | head -8' 2>&1 | head -8
docker logs litellm-mem 2>&1 | tail -5
docker rm -f litellm-mem >/dev/null 2>&1
