#!/bin/zsh -l
# spec-0.9 T8：三信号端到端断言（Mac 宿主机执行）。
# 前置：make obs-up 已就绪。流程：起 gateway → 打 /demo → 在 Tempo/Prometheus/Loki
# 三个数据源断言同一 trace_id / 指标 / 日志，全过退出 0。
set -uo pipefail
export PATH="/opt/homebrew/bin:/usr/bin:/bin:/usr/sbin:/sbin:$HOME/.local/bin:$PATH"
cd /Users/sqlrush/airush

fail() { echo "FAIL: $1"; kill ${GW_PID:-0} 2>/dev/null; exit 1; }

# Loki 查询窗口从本次运行起算，排除历史运行残留（含修复前的旧数据）
RUN_START_NS="$(( $(date +%s) - 2 ))000000000"

echo "== start gateway (OTLP -> localhost:4318) =="
AIRUSH_GATEWAY_OTLP_ENDPOINT=localhost:4318 ./bin/gateway --serve &
GW_PID=$!
for i in {1..20}; do
  curl -sf http://localhost:8081/healthz >/dev/null && break
  sleep 0.5
done
curl -sf http://localhost:8081/healthz >/dev/null || fail "gateway 未就绪"

echo "== fire /demo =="
resp=$(curl -sf http://localhost:8081/demo) || fail "/demo 请求失败"
trace_id=$(echo "$resp" | grep -oE '"trace_id":"[a-f0-9]+"' | cut -d'"' -f4)
[ -n "$trace_id" ] || fail "响应无 trace_id: $resp"
echo "trace_id=$trace_id"
curl -sf "http://localhost:8081/demo?fail=quota" >/dev/null
sleep 8   # 批处理导出间隔

echo "== assert Tempo (traces) =="
ok=0
for i in {1..10}; do
  code=$(curl -s -o /dev/null -w "%{http_code}" "http://localhost:3200/api/traces/$trace_id")
  if [ "$code" = "200" ]; then ok=1; break; fi
  sleep 2
done
[ $ok -eq 1 ] || fail "Tempo 查不到 trace $trace_id"
echo "  tempo OK"

echo "== assert Prometheus (metrics) =="
ok=0
for i in {1..10}; do
  v=$(curl -sf "http://localhost:9090/api/v1/query?query=airush_gateway_http_requests_total" | grep -o '"value"' | head -1)
  if [ -n "$v" ]; then ok=1; break; fi
  sleep 2
done
[ $ok -eq 1 ] || fail "Prometheus 查不到请求指标"
echo "  prometheus OK"

echo "== assert Loki (logs, 含 trace_id 且脱敏生效) =="
ok=0
q='{service_name="airush-gateway"}'
for i in {1..10}; do
  body=$(curl -sf -G "http://localhost:3100/loki/api/v1/query_range" \
    --data-urlencode "query=$q" --data-urlencode "limit=200" \
    --data-urlencode "start=$RUN_START_NS")
  if echo "$body" | grep -q "$trace_id"; then
    echo "$body" | grep -q "should-not-appear" && fail "Loki 日志泄漏未脱敏内容"
    ok=1; break
  fi
  sleep 2
done
[ $ok -eq 1 ] || fail "Loki 查不到含 trace_id 的日志"
echo "  loki OK（redaction 已验证）"

kill $GW_PID 2>/dev/null
echo "== OBS SMOKE ALL PASS =="
