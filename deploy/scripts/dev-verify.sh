#!/bin/zsh -l
# spec-0.10 T2/T4 验证（spec-0.12 验收复用）：kind 环境健康断言。
set -uo pipefail
export PATH="/opt/homebrew/bin:/usr/bin:/bin:/usr/sbin:/sbin:$HOME/.local/bin:$PATH"
cd /Users/sqlrush/airush
KCTL=(kubectl --context kind-airush-dev)

fail() { echo "FAIL: $1"; exit 1; }

# console_logs 取最近日志到变量再匹配。直接 `kubectl logs | grep -q` 在 pipefail 下
# 会误判：grep 命中即退出关闭管道，kubectl 收 SIGPIPE(141)，管道整体判失败——
# 日志越多越容易触发，正是采集正常时的形态。
console_logs() {
  "${KCTL[@]}" logs deploy/airush-console --since="$1" 2>/dev/null || true
}

echo "== workloads ready =="
# 按工作负载等而不是 `wait pod --all`：dev-up 末尾会 rollout restart，旧 pod 正在
# Terminating，它们永远不会变 Ready，--all 必然超时——与集群是否健康无关的假失败。
for obj in $("${KCTL[@]}" get deploy,statefulset -o name); do
  "${KCTL[@]}" rollout status "$obj" --timeout=120s >/dev/null || fail "$obj 未就绪"
done
"${KCTL[@]}" get pods --no-headers

echo "== migrate applied（领域表 + 版本） =="
# 期望版本由迁移文件数推导，不写死——写死会在每次新增迁移时无声失准
# （0004 加入时正好踩到：断言还停在 3）。
want=$(ls console/migrations/*.up.sql | wc -l | tr -d '[:space:]')
# 2>/dev/null 丢弃 psql 的 stderr：TimescaleDB 镜像会对沿用的旧数据目录报
# collation version 警告，混进捕获会把版本串解析歪。命令失败仍由 || fail 兜住。
v=$("${KCTL[@]}" exec airush-pg-0 -- psql -U postgres -d airush -tAc \
  "SELECT version || ':' || dirty FROM schema_migrations" 2>/dev/null | tr -d '[:space:]') \
  || fail "查询 schema_migrations 失败"
# 变量后面紧跟全角括号一律写 ${var}：bash 在 UTF-8 locale 下会把多字节字符
# 当成变量名的一部分，"$v（" 会被解析成变量 "v（" → unbound variable。
# 这类写法藏在 fail 分支里更阴——只有真出问题那次才炸，且炸的是脚本自己。
[ "$v" = "${want}:false" ] || fail "迁移版本异常: ${v}（期望 ${want}:false，共 $want 个迁移文件）"
t=$("${KCTL[@]}" exec airush-pg-0 -- psql -U postgres -d airush -tAc \
  "SELECT count(*) FROM information_schema.tables WHERE table_name IN ('tenants','datasources','agents')")
[ "$t" = "3" ] || fail "领域表缺失（tenants/datasources/agents 应为 3，得 ${t}）"
col=$("${KCTL[@]}" exec airush-pg-0 -- psql -U postgres -d airush -tAc \
  "SELECT count(*) FROM information_schema.columns WHERE table_name='connectors' AND column_name='enroll_token_hash'")
[ "$col" = "1" ] || fail "connectors.enroll_token_hash 缺失（0003 未应用）"
seed=$("${KCTL[@]}" exec airush-pg-0 -- psql -U postgres -d airush -tAc \
  "SELECT count(*) FROM tenants WHERE slug='dev'")
[ "$seed" = "1" ] || fail "dev 租户 seed 缺失"
echo "  migrate version=$v, 领域表/seed/接入列就位"

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
# 开发环境启用 pg_stat_statements，让慢查询快照（spec-1.4）走成功路径而非能力降级。
# 仅 dev：控制面库在生产不是被采数据源，故不进 migration。
"${KCTL[@]}" exec airush-pg-0 -- psql -U postgres -d airush -c \
  "CREATE EXTENSION IF NOT EXISTS pg_stat_statements" >/dev/null 2>&1 || true
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
  if printf '%s' "$(console_logs 120s)" | grep -q "metrics collected.*$cdsid"; then ok="1"; break; fi
  sleep 5
done
[ -n "$ok" ] || fail "采集器未对 dev-verify-collect 采到批（console 日志无 metrics collected）"
echo "  指标采集 OK（Direct 通道对内置 PG 周期采集心跳可见）"

echo "== 快照采集（spec-1.4：慢日志/表结构/配置三类） =="
# dev values 把快照间隔压到 60s/300s，配合抖动最长约 5 分钟内各出一次心跳。
for kind in slowlog schema config; do
  ok=""
  for i in $(seq 1 40); do
    # console 输出结构化 JSON 日志，故匹配 "kind":"<kind>"（兼容 logfmt 的 kind=）。
    if printf '%s' "$(console_logs 600s)" \
      | grep -Eq "metrics collected.*$cdsid.*(\"kind\":\"$kind\"|kind=$kind)"; then ok="1"; break; fi
    sleep 10
  done
  [ -n "$ok" ] || fail "快照采集未见 kind=$kind 心跳（console 日志）"
  echo "  快照 $kind OK"
done

echo "== 落库与查询面（spec-1.5：采集数据真的进了 TimescaleDB 并读得出来） =="
# 前面几段验的是"采到了"（日志心跳），这一段验的是"存住了且查得出来"——
# 采集正常但落库静默失败，是最容易漏过去的一类故障：日志一片祥和，数据一片空白。
tsver=$("${KCTL[@]}" exec airush-pg-0 -- psql -U postgres -d airush -tAc \
  "SELECT extversion FROM pg_extension WHERE extname='timescaledb'" | tr -d '[:space:]')
[ -n "$tsver" ] || fail "timescaledb 扩展未安装（0004 迁移未生效？）"
echo "  timescaledb 扩展 OK（${tsver}）"

# 隔离形态自检：应用角色对基表 schema 必须无 USAGE（AD-10 等效形态第一道锁）。
usage=$("${KCTL[@]}" exec airush-pg-0 -- psql -U postgres -d airush -tAc \
  "SELECT has_schema_privilege('airush_app','tsdb','USAGE')" | tr -d '[:space:]')
[ "$usage" = "f" ] || fail "airush_app 对 tsdb schema 有 USAGE —— 等效隔离第一道锁失效"
echo "  租户隔离基线 OK（应用角色够不到基表 schema）"

"${KCTL[@]}" port-forward svc/airush-console 18080:8080 >/dev/null 2>&1 &
PF=$!
sleep 2
ok=""
for i in $(seq 1 30); do
  n=$(curl -sf "http://localhost:18080/api/v1/datasources/$cdsid/series?name=db.connections.active&step=1h" \
    | grep -o '"at"' | wc -l | tr -d '[:space:]')
  if [ "${n:-0}" -ge 1 ]; then ok="1"; break; fi
  sleep 10
done
[ -n "$ok" ] || { kill $PF 2>/dev/null; fail "查询面未返回任何指标点——采到了但没落库（或没读出来）"; }
echo "  指标落库 + 查询面 OK"

# 表结构快照必须能取到当前版本（慢查询走 series 面，故不在此路径）。
code=$(curl -s -o /tmp/airush-snap.json -w '%{http_code}' \
  "http://localhost:18080/api/v1/datasources/$cdsid/snapshots/schema")
kill $PF 2>/dev/null
[ "$code" = "200" ] || fail "表结构快照未落库 http=$code $(cat /tmp/airush-snap.json)"
grep -q '"tables"' /tmp/airush-snap.json || fail "快照 payload 无 tables 字段"
echo "  快照落库 + 查询面 OK"

echo "== connector 接入 e2e（spec-1.2：enroll → session → online；spec-1.5：通道落库） =="
# 幂等前置（spec-0.12 §3 从零语义）：清理上次遗留的 dev-verify-conn。
# 数据源必须先删——datasources.connector_id 的外键无 ON DELETE，先删连接器会被 RESTRICT 拦住。
"${KCTL[@]}" exec airush-pg-0 -- psql -U postgres -d airush -c \
  "DELETE FROM datasources WHERE name='dev-verify-conn-ds'" >/dev/null 2>&1
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
        # 被采库凭据只出现在**连接器侧**（AD-4：Connector 模式凭据不入平台）。
        # 这里用的是 kind 内置 PG 的开发口令，与 values-dev.yaml 同一个值。
        - { name: AIRUSH_CONNECTOR_DB_URL, value: "postgres://postgres:airush-dev-pg@airush-pg:5432/airush?sslmode=disable" }
YAML
ok=""
for i in $(seq 1 30); do
  st=$("${KCTL[@]}" exec airush-pg-0 -- psql -U postgres -d airush -tAc \
    "SELECT status FROM connectors WHERE id='$cid'" | tr -d '[:space:]')
  if [ "$st" = "online" ]; then ok="1"; break; fi
  sleep 2
done
[ -n "$ok" ] || { "${KCTL[@]}" delete pod airush-devverify-connector --ignore-not-found >/dev/null 2>&1
  fail "connector 未达 online 状态（末次: ${st:-none}）"; }
echo "  connector e2e OK（enroll 签证 → mTLS 会话 → 心跳 → online）"

# spec-1.5 T22：Connector 通道采集也要落库。与 T21（Direct 通道）合起来证明
# 两条通道在存储侧等价——Connector 侧多了 gateway → console 内部 API 这一跳
# （§8 Q5 选项 A：gateway 不持有 DB 连接），这一跳出问题只会表现为"没数据"。
"${KCTL[@]}" port-forward svc/airush-console 18080:8080 >/dev/null 2>&1 &
PF=$!
sleep 2
code=$(curl -s -o /tmp/airush-conn-ds.json -w '%{http_code}' -X POST http://localhost:18080/api/v1/datasources \
  -H 'Content-Type: application/json' -d "{
    \"name\":\"dev-verify-conn-ds\",\"engine_family\":\"postgres\",\"engine\":\"postgres\",
    \"connect_mode\":\"connector\",\"connector_id\":\"${cid}\",
    \"host\":\"airush-pg\",\"port\":5432,\"database_name\":\"airush\"}")
if [ "$code" != "201" ] && [ "$code" != "409" ]; then
  kill $PF 2>/dev/null
  "${KCTL[@]}" delete pod airush-devverify-connector --ignore-not-found >/dev/null 2>&1
  fail "创建 connector 模式数据源失败 http=${code} $(cat /tmp/airush-conn-ds.json)"
fi
conndsid=$(curl -sf http://localhost:18080/api/v1/datasources \
  | sed -n 's/.*"id":"\([0-9a-f-]*\)","name":"dev-verify-conn-ds".*/\1/p')
# 显式判空：id 取空时下面的 URL 会变成 /datasources//series，稳定 404 → 循环超时，
# 报出来的却是"通道没落库"。那条错误信息会把人带去查错方向。
if [ -z "$conndsid" ]; then
  kill $PF 2>/dev/null
  "${KCTL[@]}" delete pod airush-devverify-connector --ignore-not-found >/dev/null 2>&1
  fail "未取到 dev-verify-conn-ds 数据源 id"
fi
ok=""
for i in $(seq 1 30); do
  n=$(curl -sf "http://localhost:18080/api/v1/datasources/$conndsid/series?name=db.connections.active&step=1h" \
    | grep -o '"at"' | wc -l | tr -d '[:space:]')
  if [ "${n:-0}" -ge 1 ]; then ok="1"; break; fi
  sleep 10
done
kill $PF 2>/dev/null
"${KCTL[@]}" delete pod airush-devverify-connector --ignore-not-found >/dev/null 2>&1
[ -n "$ok" ] || fail "Connector 通道采集未落库——gateway → console 上报这一跳断了（spec-1.5 T22）"
echo "  Connector 通道落库 OK（连接器直采客户库 → gateway → console → TimescaleDB）"

echo "== LLM 网关（spec-1.7 T19/T20：LiteLLM 无状态形态就绪 + 经 master key 路由到 mock + 无明文 key） =="
"${KCTL[@]}" rollout status deploy/airush-llm --timeout=180s >/dev/null || fail "airush-llm 未就绪"
# T19：readiness 必须报 db 未连接——确认没人"顺手"给它配了 DATABASE_URL（那会让 prompt 进它的库，AD-3）
"${KCTL[@]}" port-forward svc/airush-llm 14000:4000 >/dev/null 2>&1 &
PF=$!
sleep 2
ready=$(curl -s http://localhost:14000/health/readiness)
echo "$ready" | grep -q '"db":"Not connected"' || { kill $PF 2>/dev/null; fail "LiteLLM readiness 未报 db 未连接: ${ready}（无状态形态被破坏？）"; }
echo "  LiteLLM 就绪且无 DB（${ready}）"
# T20：master key 来自 Secret（脚本里绝不出现字面量）；经逻辑模型名打到 mock sidecar
mk=$("${KCTL[@]}" get secret airush-llm-master-key -o jsonpath='{.data.master-key}' | base64 -d)
[ -n "$mk" ] || { kill $PF 2>/dev/null; fail "master key Secret 为空"; }
nokey=$(curl -s -o /dev/null -w '%{http_code}' http://localhost:14000/v1/models)
[ "$nokey" = "401" ] || { kill $PF 2>/dev/null; fail "无 key 访问未被拒（http=${nokey}）"; }
resp=$(curl -s http://localhost:14000/v1/chat/completions -H "Authorization: Bearer $mk" \
  -H 'Content-Type: application/json' -d '{"model":"chat-default","messages":[{"role":"user","content":"ping"}]}')
echo "$resp" | grep -q '"content":"mock reply"' || { kill $PF 2>/dev/null; fail "chat-default 未路由到 mock: $(echo "$resp" | head -c 300)"; }
echo "$resp" | grep -q '"total_tokens":14' || { kill $PF 2>/dev/null; fail "usage 未透传: $(echo "$resp" | head -c 300)"; }
# fallback：chat-fail 上游恒 500 → 备选 chat-default 接住
resp=$(curl -s http://localhost:14000/v1/chat/completions -H "Authorization: Bearer $mk" \
  -H 'Content-Type: application/json' -d '{"model":"chat-fail","messages":[{"role":"user","content":"ping"}]}')
echo "$resp" | grep -q '"content":"mock reply"' || { kill $PF 2>/dev/null; fail "fallback 未生效: $(echo "$resp" | head -c 300)"; }
# Responses API 经供应商原生前缀桥接（实测结论进 e2e）
code=$(curl -s -o /tmp/airush-llm-resp.json -w '%{http_code}' http://localhost:14000/v1/responses -H "Authorization: Bearer $mk" \
  -H 'Content-Type: application/json' -d '{"model":"chat-default","input":"ping"}')
kill $PF 2>/dev/null
[ "$code" = "200" ] || fail "Responses API 桥接失败 http=${code} $(head -c 300 /tmp/airush-llm-resp.json)"
# 渲染物无明文 key：ConfigMap 里只允许 os.environ 引用与 dev 的 "mock" 占位
cm=$("${KCTL[@]}" get configmap airush-llm-config -o jsonpath='{.data.config\.yaml}')
echo "$cm" | grep -E 'api_key|master_key' | grep -vE 'os\.environ/|api_key: mock$' && fail "LLM ConfigMap 出现明文 key"
echo "  LLM 路由 OK（无 key 401 / chat-default→mock 含 usage / chat-fail→fallback / Responses 桥接 200 / ConfigMap 无明文 key）"

# T21：控制面配额面——默认租户 seed 行在；PUT 改预算 → GET 回读；用量聚合端点可达
"${KCTL[@]}" port-forward svc/airush-console 18080:8080 >/dev/null 2>&1 &
PF=$!
sleep 2
q=$(curl -sf http://localhost:18080/api/v1/llm/quota) || { kill $PF; fail "GET /api/v1/llm/quota 不可达"; }
echo "$q" | grep -q '"set":true' || { kill $PF; fail "默认租户无配额 seed 行: ${q}"; }
code=$(curl -s -o /tmp/airush-llm-quota.json -w '%{http_code}' -X PUT http://localhost:18080/api/v1/llm/quota \
  -H 'Content-Type: application/json' -d '{"token_budget":123456,"hard_stop":true}')
[ "$code" = "200" ] || { kill $PF; fail "PUT quota http=${code} $(cat /tmp/airush-llm-quota.json)"; }
q=$(curl -sf http://localhost:18080/api/v1/llm/quota)
echo "$q" | grep -q '"token_budget":123456' || { kill $PF; fail "PUT 后回读不一致: ${q}"; }
# 复原为默认，别把 dev 环境留在小预算上
curl -s -o /dev/null -X PUT http://localhost:18080/api/v1/llm/quota -H 'Content-Type: application/json' \
  -d '{"token_budget":50000000,"hard_stop":true}'
u=$(curl -sf "http://localhost:18080/api/v1/llm/usage?group_by=model") || { kill $PF; fail "GET /api/v1/llm/usage 不可达"; }
kill $PF 2>/dev/null
echo "$u" | grep -q '"items"' || fail "usage 响应形态异常: ${u}"
echo "  LLM 配额面 OK（seed 行在 / PUT→GET 回读 / usage 聚合可达）"

echo "== helm 幂等（再次 upgrade 应零变更零重启） =="
before=$("${KCTL[@]}" get pods -o jsonpath='{range .items[*]}{.metadata.name}{"\n"}{end}' | sort)
helm upgrade --install airush deploy/charts/airush --kube-context kind-airush-dev \
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
