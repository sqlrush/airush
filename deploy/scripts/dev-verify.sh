#!/bin/zsh -l
# spec-0.10 T2/T4 验证（spec-0.12 验收复用）：kind 环境健康断言。
set -uo pipefail
export PATH="/opt/homebrew/bin:/usr/bin:/bin:/usr/sbin:/sbin:$HOME/.local/bin:$PATH"
cd /Users/sqlrush/airush
KCTL=(kubectl --context kind-airush-dev)

fail() { echo "FAIL: $1"; exit 1; }

echo "== pods ready =="
"${KCTL[@]}" wait --for=condition=Ready pod --all --timeout=120s >/dev/null || fail "存在未就绪 pod"
"${KCTL[@]}" get pods --no-headers

echo "== migrate applied（tenants 表 + 版本） =="
v=$("${KCTL[@]}" exec airush-pg-0 -- psql -U postgres -d airush -tAc \
  "SELECT version || ':' || dirty FROM schema_migrations") || fail "查询 schema_migrations 失败"
[ "$v" = "1:false" ] || fail "迁移版本异常: $v"
t=$("${KCTL[@]}" exec airush-pg-0 -- psql -U postgres -d airush -tAc \
  "SELECT count(*) FROM information_schema.tables WHERE table_name='tenants'")
[ "$t" = "1" ] || fail "tenants 表缺失"
echo "  migrate version=1 dirty=false, tenants 就位"

echo "== gateway /healthz（port-forward） =="
"${KCTL[@]}" port-forward svc/airush-gateway 18081:8081 >/dev/null 2>&1 &
PF=$!
sleep 2
resp=$(curl -sf http://localhost:18081/healthz) || { kill $PF; fail "/healthz 不可达"; }
kill $PF 2>/dev/null
echo "  $resp"

echo "== helm 幂等（再次 upgrade 应零变更零重启） =="
before=$("${KCTL[@]}" get pods -o jsonpath='{range .items[*]}{.metadata.name}{"\n"}{end}' | sort)
helm upgrade --install airush deploy/charts/airush \
  -f deploy/charts/airush/values-dev.yaml --wait --timeout 3m >/dev/null || fail "重复 upgrade 失败"
after=$("${KCTL[@]}" get pods -o jsonpath='{range .items[*]}{.metadata.name}{"\n"}{end}' | sort)
[ "$before" = "$after" ] || fail "幂等 upgrade 引发 pod 重建"
echo "  幂等 OK（pod 集合不变）"

echo "== 安全上下文（nonroot/只读 rootfs） =="
sc=$("${KCTL[@]}" get deploy airush-gateway -o jsonpath='{.spec.template.spec.containers[0].securityContext}')
echo "$sc" | grep -q '"readOnlyRootFilesystem":true' || fail "只读 rootfs 未生效"
echo "$sc" | grep -q '"runAsNonRoot":true' || fail "nonroot 未生效"
echo "  securityContext OK"

echo "== DEV VERIFY ALL PASS =="
