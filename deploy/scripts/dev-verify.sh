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

echo "== migrate applied（领域表 + 版本） =="
v=$("${KCTL[@]}" exec airush-pg-0 -- psql -U postgres -d airush -tAc \
  "SELECT version || ':' || dirty FROM schema_migrations") || fail "查询 schema_migrations 失败"
[ "$v" = "2:false" ] || fail "迁移版本异常: $v（期望 2:false，spec-1.1 0002 就位）"
t=$("${KCTL[@]}" exec airush-pg-0 -- psql -U postgres -d airush -tAc \
  "SELECT count(*) FROM information_schema.tables WHERE table_name IN ('tenants','datasources','agents')")
[ "$t" = "3" ] || fail "领域表缺失（tenants/datasources/agents 应为 3，得 $t）"
seed=$("${KCTL[@]}" exec airush-pg-0 -- psql -U postgres -d airush -tAc \
  "SELECT count(*) FROM tenants WHERE slug='dev'")
[ "$seed" = "1" ] || fail "dev 租户 seed 缺失"
echo "  migrate version=2 dirty=false, 领域表与 seed 就位"

echo "== gateway /healthz（port-forward） =="
"${KCTL[@]}" port-forward svc/airush-gateway 18081:8081 >/dev/null 2>&1 &
PF=$!
sleep 2
resp=$(curl -sf http://localhost:18081/healthz) || { kill $PF; fail "/healthz 不可达"; }
kill $PF 2>/dev/null
echo "  $resp"

echo "== console API 端到端（spec-1.1：healthz + 直连数据源全链路） =="
"${KCTL[@]}" port-forward svc/airush-console 18080:8080 >/dev/null 2>&1 &
PF=$!
sleep 2
resp=$(curl -sf http://localhost:18080/healthz) || { kill $PF; fail "console /healthz 不可达"; }
echo "  $resp"
code=$(curl -s -o /tmp/airush-ds.json -w '%{http_code}' -X POST http://localhost:18080/api/v1/datasources \
  -H 'Content-Type: application/json' -d '{
    "name":"dev-verify-og","engine_family":"postgres","engine":"opengauss",
    "connect_mode":"direct","host":"10.0.0.9","port":5432,
    "credential":{"username":"dba","password":"dev-verify-secret"}}')
if [ "$code" != "201" ] && [ "$code" != "409" ]; then kill $PF; fail "创建数据源失败 http=$code $(cat /tmp/airush-ds.json)"; fi
n=$(curl -sf http://localhost:18080/api/v1/datasources | grep -o '"name":"dev-verify-og"' | wc -l | tr -d ' ')
kill $PF 2>/dev/null
[ "$n" = "1" ] || fail "数据源列表未见 dev-verify-og"
leak=$("${KCTL[@]}" exec airush-pg-0 -- psql -U postgres -d airush -tAc \
  "SELECT count(*) FROM datasource_credentials WHERE position('dev-verify-secret'::bytea in secret_ciphertext) > 0")
[ "$leak" = "0" ] || fail "凭据明文出现在密文列（AD-4② 违规）"
echo "  console API OK（201/幂等 409、列表可见、密文无明文）"

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
