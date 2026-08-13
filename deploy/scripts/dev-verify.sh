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
[ "$v" = "3:false" ] || fail "迁移版本异常: $v（期望 3:false，spec-1.2 0003 就位）"
t=$("${KCTL[@]}" exec airush-pg-0 -- psql -U postgres -d airush -tAc \
  "SELECT count(*) FROM information_schema.tables WHERE table_name IN ('tenants','datasources','agents')")
[ "$t" = "3" ] || fail "领域表缺失（tenants/datasources/agents 应为 3，得 $t）"
col=$("${KCTL[@]}" exec airush-pg-0 -- psql -U postgres -d airush -tAc \
  "SELECT count(*) FROM information_schema.columns WHERE table_name='connectors' AND column_name='enroll_token_hash'")
[ "$col" = "1" ] || fail "connectors.enroll_token_hash 缺失（0003 未应用）"
seed=$("${KCTL[@]}" exec airush-pg-0 -- psql -U postgres -d airush -tAc \
  "SELECT count(*) FROM tenants WHERE slug='dev'")
[ "$seed" = "1" ] || fail "dev 租户 seed 缺失"
echo "  migrate version=3 dirty=false, 领域表/seed/接入列就位"

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
[ "$n" = "1" ] || { kill $PF 2>/dev/null; fail "数据源列表未见 dev-verify-og"; }
leak=$("${KCTL[@]}" exec airush-pg-0 -- psql -U postgres -d airush -tAc \
  "SELECT count(*) FROM datasource_credentials WHERE position('dev-verify-secret'::bytea in secret_ciphertext) > 0")
[ "$leak" = "0" ] || fail "凭据明文出现在密文列（AD-4② 违规）"
# spec-1.17：test-connection（dev-verify-og host 不可达）→ 连接失败错误码，响应无凭据明文
dsid=$(curl -sf http://localhost:18080/api/v1/datasources | sed -n 's/.*"id":"\([0-9a-f-]*\)","name":"dev-verify-og".*/\1/p')
tc=$(curl -s -X POST http://localhost:18080/api/v1/datasources/$dsid/test-connection)
kill $PF 2>/dev/null
echo "$tc" | grep -qE 'AR_DATASOURCE_(CONNECT_FAILED|TEST_TIMEOUT)' || fail "test-connection 错误码异常: $tc"
echo "$tc" | grep -q 'dev-verify-secret' && fail "test-connection 响应泄漏凭据明文"
echo "  console API OK（201/幂等 409、列表可见、密文无明文、test-connection 错误码+无泄漏）"

echo "== 指标采集（spec-1.3：Direct 通道采一批） =="
"${KCTL[@]}" port-forward svc/airush-console 18080:8080 >/dev/null 2>&1 &
PF=$!
sleep 2
# 建可达 Direct 数据源（指向内置 PG）→ 采集器周期采集
code=$(curl -s -o /tmp/airush-collect-ds.json -w '%{http_code}' -X POST http://localhost:18080/api/v1/datasources \
  -H 'Content-Type: application/json' -d '{
    "name":"dev-verify-collect","engine_family":"postgres","engine":"postgres",
    "connect_mode":"direct","host":"airush-pg","port":5432,"database_name":"airush",
    "credential":{"username":"postgres","password":"airush-dev-pg"}}')
if [ "$code" != "201" ] && [ "$code" != "409" ]; then kill $PF 2>/dev/null; fail "创建采集数据源失败 http=$code $(cat /tmp/airush-collect-ds.json)"; fi
cdsid=$(curl -sf http://localhost:18080/api/v1/datasources | sed -n 's/.*"id":"\([0-9a-f-]*\)","name":"dev-verify-collect".*/\1/p')
kill $PF 2>/dev/null
[ -n "$cdsid" ] || fail "未取到 dev-verify-collect 数据源 id"
# 采集器周期采集（dev interval=15s + 抖动）→ console 日志出现该数据源采集心跳
ok=""
for i in $(seq 1 12); do
  if "${KCTL[@]}" logs deploy/airush-console --since=120s 2>/dev/null | grep -q "metrics collected.*$cdsid"; then ok="1"; break; fi
  sleep 5
done
[ -n "$ok" ] || fail "采集器未对 dev-verify-collect 采到批（console 日志无 metrics collected）"
echo "  指标采集 OK（Direct 通道对内置 PG 周期采集心跳可见）"

echo "== connector 接入 e2e（spec-1.2：enroll → session → online） =="
# 幂等前置（spec-0.12 §3 从零语义）：清理上次遗留的 dev-verify-conn
"${KCTL[@]}" exec airush-pg-0 -- psql -U postgres -d airush -c \
  "DELETE FROM connectors WHERE name='dev-verify-conn'" >/dev/null 2>&1
"${KCTL[@]}" port-forward svc/airush-console 18080:8080 >/dev/null 2>&1 &
PF=$!
sleep 2
tok=$(curl -sf -X POST http://localhost:18080/api/v1/connectors \
  -H 'Content-Type: application/json' -d '{"name":"dev-verify-conn","location":"kind"}' \
  | sed -n 's/.*"enrollment_token":"\([^"]*\)".*/\1/p')
cid=$("${KCTL[@]}" exec airush-pg-0 -- psql -U postgres -d airush -tAc \
  "SELECT id FROM connectors WHERE name='dev-verify-conn'" | tr -d '[:space:]')
kill $PF 2>/dev/null
[ -n "$tok" ] || fail "未取得 enrollment token"
# 在集群内跑 connector：initContainer 注册（写证书到 emptyDir），主容器维持会话。
# distroless 无 shell，故用 initContainer + 共享卷（entrypoint=/app）。
"${KCTL[@]}" delete pod airush-devverify-connector --ignore-not-found >/dev/null 2>&1
img="${REGISTRY:-ghcr.io/sqlrush/airush}/connector:latest"
"${KCTL[@]}" apply -f - >/dev/null <<YAML
apiVersion: v1
kind: Pod
metadata:
  name: airush-devverify-connector
spec:
  restartPolicy: Never
  volumes:
    - name: conn
      emptyDir: {}
  initContainers:
    - name: enroll
      image: ${img}
      imagePullPolicy: Never
      args: ["--enroll"]
      volumeMounts:
        - { name: conn, mountPath: /tmp/conn }
      env:
        - { name: AIRUSH_CONNECTOR_CONFIG_DIR, value: /tmp/conn }
        - { name: AIRUSH_CONNECTOR_ENROLL_ADDR, value: "airush-gateway:8082" }
        - { name: AIRUSH_CONNECTOR_SESSION_ADDR, value: "airush-gateway:8083" }
        - { name: AIRUSH_CONNECTOR_ENROLL_TOKEN, value: "${tok}" }
        - name: AIRUSH_CONNECTOR_ENROLL_CA_PEM
          valueFrom:
            secretKeyRef: { name: airush-connector-pki, key: ca.crt }
  containers:
    - name: run
      image: ${img}
      imagePullPolicy: Never
      args: ["--run"]
      volumeMounts:
        - { name: conn, mountPath: /tmp/conn }
      env:
        - { name: AIRUSH_CONNECTOR_CONFIG_DIR, value: /tmp/conn }
        - { name: AIRUSH_CONNECTOR_ENROLL_ADDR, value: "airush-gateway:8082" }
        - { name: AIRUSH_CONNECTOR_SESSION_ADDR, value: "airush-gateway:8083" }
YAML
ok=""
for i in $(seq 1 30); do
  st=$("${KCTL[@]}" exec airush-pg-0 -- psql -U postgres -d airush -tAc \
    "SELECT status FROM connectors WHERE id='$cid'" | tr -d '[:space:]')
  if [ "$st" = "online" ]; then ok="1"; break; fi
  sleep 2
done
"${KCTL[@]}" delete pod airush-devverify-connector --ignore-not-found >/dev/null 2>&1
[ -n "$ok" ] || fail "connector 未达 online 状态（末次: ${st:-none}）"
echo "  connector e2e OK（enroll 签证 → mTLS 会话 → 心跳 → online）"

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
