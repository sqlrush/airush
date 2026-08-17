#!/bin/zsh -l
# spec-1.8 DoD "Kimi K3 金丝雀"：经 console → agent-runtime → LiteLLM（kind）→ 真 Kimi K3 跑一轮真对话，
# 且要求模型先调一次工具（codexgo 内置 update_plan：无 MCP 也存在的真实 function_call 回合），
# 断言：SSE 收到 task_complete、事件流里有 function_call + function_call_output、助手回复非 mock、
# Meter 记账增长、日志无 key 片段。前置：kind 栈已起（mac-dev-up.sh）+ mac-llm-real-provider.sh 已把 chat-kimi 装进网关。
# key 只从 ~/.airush/kimi.key 读一个前缀做"泄漏"grep，值本身不出现在任何输出。
set -uo pipefail
export PATH="/opt/homebrew/bin:/usr/local/bin:/usr/bin:/bin:$PATH"
cd /Users/sqlrush/airush
K=(kubectl --context kind-airush-dev)
fail() { echo "FAIL: $1"; exit 1; }
[ -s ~/.airush/kimi.key ] || fail "缺 ~/.airush/kimi.key"
"${K[@]}" get configmap airush-llm-config -o jsonpath='{.data.config\.yaml}' | grep -q 'chat-kimi' || fail "网关无 chat-kimi（先跑 mac-llm-real-provider.sh）"
"${K[@]}" rollout status deploy/airush-agent-runtime --timeout=120s >/dev/null || fail "agent-runtime 未就绪"

"${K[@]}" port-forward svc/airush-console 18080:8080 >/dev/null 2>&1 &
PF=$!; sleep 2
before=$(curl -sf http://localhost:18080/api/v1/llm/quota | grep -o '"used_this_month":[0-9]*' | cut -d: -f2)
code=$(curl -s -o /tmp/airush-canary-thread.json -w '%{http_code}' -X POST http://localhost:18080/api/v1/agent/threads \
  -H 'Content-Type: application/json' -d '{"title":"kimi canary","model":"chat-kimi"}')
[ "$code" = "201" ] || { kill $PF; fail "建线程 http=$code $(cat /tmp/airush-canary-thread.json)"; }
tid=$(grep -o '"thread_id":"[^"]*"' /tmp/airush-canary-thread.json | cut -d'"' -f4)
prompt='你必须先调用 update_plan 工具（plan 里两步：自我介绍、说明能力，状态 pending），工具返回后再用两句中文回答。不调用工具就直接回答是错误的。'
code=$(curl -s -o /tmp/airush-canary-turn.json -w '%{http_code}' -X POST "http://localhost:18080/api/v1/agent/threads/$tid/turns" \
  -H 'Content-Type: application/json' -d "{\"input\":[{\"type\":\"text\",\"text\":\"$prompt\"}]}")
[ "$code" = "200" ] || { kill $PF; fail "发 turn http=$code $(cat /tmp/airush-canary-turn.json)"; }
curl -sN --max-time 120 "http://localhost:18080/api/v1/agent/threads/$tid/events?from_seq=1" > /tmp/airush-canary-sse.txt 2>/dev/null &
SSEPID=$!
for i in $(seq 1 120); do
  grep -q "event: task_complete" /tmp/airush-canary-sse.txt 2>/dev/null && break
  sleep 1
done
kill $SSEPID 2>/dev/null
grep -q "event: task_complete" /tmp/airush-canary-sse.txt || { kill $PF; fail "120s 内未收到 task_complete: $(tail -c 800 /tmp/airush-canary-sse.txt)"; }
after=$(curl -sf http://localhost:18080/api/v1/llm/quota | grep -o '"used_this_month":[0-9]*' | cut -d: -f2)
kill $PF 2>/dev/null

echo "== 事件流形态 =="
grep -o "event: [a-z_]*" /tmp/airush-canary-sse.txt | sort | uniq -c | sort -rn | head -20
if grep -q "^event: error" /tmp/airush-canary-sse.txt; then
  grep -A1 "^event: error" /tmp/airush-canary-sse.txt | head -c 800; echo
  fail "事件流里有 error 事件（见上）——turn 没有干净跑完"
fi
calls=$("${K[@]}" exec airush-pg-0 -- psql -U postgres -d airush -tAc \
  "SELECT count(*) FROM agent_rollout_events WHERE thread_id = '$tid' AND event_type = 'response_item' AND payload::text LIKE '%\"function_call\"%'" | tr -d '[:space:]')
outs=$("${K[@]}" exec airush-pg-0 -- psql -U postgres -d airush -tAc \
  "SELECT count(*) FROM agent_rollout_events WHERE thread_id = '$tid' AND event_type = 'response_item' AND payload::text LIKE '%\"function_call_output\"%'" | tr -d '[:space:]')
[ "${calls:-0}" -ge 1 ] && [ "${outs:-0}" -ge 1 ] || fail "没有工具调用回合（function_call=${calls:-0} output=${outs:-0}）——桥接的 tool 回合没通"
reply=$(grep -A1 "^event: agent_message" /tmp/airush-canary-sse.txt | grep -o '"message":"[^"]*"' | tail -1 | head -c 400)
echo "== 助手最后一条 agent_message：$reply"
echo "$reply" | grep -q "mock reply" && fail "回复是 mock，没打到真 Kimi"
[ -n "$reply" ] || fail "没有 agent_message"
[ "${after:-0}" -gt "${before:-0}" ] || fail "用量未增长（before=${before:-0} after=${after:-0}）"
echo "== Meter 记账：used_this_month ${before:-0} → ${after:-0} =="
leak=$("${K[@]}" logs deploy/airush-agent-runtime --tail=3000 2>/dev/null | grep -c "$(cut -c1-12 ~/.airush/kimi.key)" || true)
leak2=$("${K[@]}" logs deploy/airush-llm -c litellm --tail=3000 2>/dev/null | grep -c "$(cut -c1-12 ~/.airush/kimi.key)" || true)
[ "${leak:-0}" = "0" ] && [ "${leak2:-0}" = "0" ] || fail "日志出现 key 片段（runtime=${leak} litellm=${leak2}）"
echo "== KIMI CANARY PASS（function_call=${calls} output=${outs}，日志无 key）=="
