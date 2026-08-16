#!/bin/zsh
# 把真实供应商 key 装进 kind（Secret）并以仓库外的 values 覆盖启用真实模型 chat-kimi。
# key 只从 ~/.airush/kimi.key 读；values 覆盖文件写在 ~/.airush/，不进仓库。
# 用法：mac-llm-real-provider.sh [verify]  —— 带 verify 则升级后经网关调一次 chat-kimi。
set -eu
export PATH="/opt/homebrew/bin:/usr/local/bin:$PATH"
K=(kubectl --context kind-airush-dev)
[ -s ~/.airush/kimi.key ] || { echo "缺 ~/.airush/kimi.key"; exit 1; }
"${K[@]}" create secret generic airush-llm-provider-keys \
  --from-file=KIMI_API_KEY=$HOME/.airush/kimi.key --dry-run=client -o yaml | "${K[@]}" apply -f - >/dev/null
umask 077
cat > ~/.airush/values-kimi.yaml <<'YAML'
# 仓库外本地覆盖：在 dev（mockProvider）形态里追加一条真实供应商模型 chat-kimi（real: true 不被改写成 mock）
llm:
  providerKeysSecret: airush-llm-provider-keys
  models:
    - name: chat-default
      litellm: deepseek/mock-default
      apiKeyEnv: MOCK
    - name: chat-long
      litellm: deepseek/mock-long
      apiKeyEnv: MOCK
    - name: chat-fail
      litellm: deepseek/mock-fail
      apiKeyEnv: MOCK
    - name: chat-kimi
      real: true
      litellm: moonshot/k3
      apiBase: https://api.kimi.com/coding/v1
      apiKeyEnv: KIMI_API_KEY
YAML
cd /Users/sqlrush/airush
helm upgrade --install airush deploy/charts/airush --kube-context kind-airush-dev \
  -f deploy/charts/airush/values-dev.yaml -f ~/.airush/values-kimi.yaml --wait --timeout 5m >/dev/null
"${K[@]}" rollout status deploy/airush-llm --timeout=180s
echo "== ConfigMap 里 chat-kimi 条目（应为 os.environ 引用，无明文）=="
"${K[@]}" get configmap airush-llm-config -o jsonpath='{.data.config\.yaml}' | grep -A4 'chat-kimi'
if [ "${1:-}" = "verify" ]; then
  mk=$("${K[@]}" get secret airush-llm-master-key -o jsonpath='{.data.master-key}' | base64 -d)
  "${K[@]}" port-forward svc/airush-llm 14000:4000 >/dev/null 2>&1 &
  PF=$!; sleep 2
  echo "== 经集群内网关调 chat-kimi（真 Kimi K3）=="
  curl -s http://localhost:14000/v1/chat/completions -H "Authorization: Bearer $mk" -H 'Content-Type: application/json' \
    -d '{"model":"chat-kimi","messages":[{"role":"user","content":"用一句话说明你是什么模型"}],"max_tokens":300}' | head -c 700; echo
  echo "== Responses + tools 经网关 =="
  curl -s http://localhost:14000/v1/responses -H "Authorization: Bearer $mk" -H 'Content-Type: application/json' \
    -d '{"model":"chat-kimi","input":"查一下 db.connections.active 当前值","tools":[{"type":"function","name":"lookup_metric","parameters":{"type":"object","properties":{"name":{"type":"string"}},"required":["name"]}}]}' | grep -o '"type":"function_call"\|"name":"lookup_metric"' | sort -u
  kill $PF 2>/dev/null
  echo "== LiteLLM 日志含 key 片段？（应为 0）=="
  "${K[@]}" logs deploy/airush-llm -c litellm --tail=2000 2>/dev/null | grep -c "$(cut -c1-12 ~/.airush/kimi.key)" || true
fi
