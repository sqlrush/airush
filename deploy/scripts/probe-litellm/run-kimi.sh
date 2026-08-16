#!/bin/zsh
# 真实供应商探测：Kimi。用法: run-kimi.sh [model-id]（默认按 config-kimi.yaml；传参则替换两条的模型 id）
# key 从 ~/.airush/kimi.key 读（仓库外，0600），绝不出现在脚本/配置/日志里。
set -u
export PATH="/opt/homebrew/bin:/usr/local/bin:$PATH"
cd /Users/sqlrush/airush/deploy/scripts/probe-litellm
IMG=ghcr.io/berriai/litellm@sha256:154e23bb5f31b1f10e16392a8ef299bd2cde08de3a64a6849002cfcc25ce3c63
MK=${LITELLM_PROBE_MASTER_KEY:-probe-master-key-not-a-secret}
KEY=$(cat ~/.airush/kimi.key)
CFG="$PWD/config-kimi.yaml"
if [ -n "${1:-}" ]; then
  sed "s#/k3#/$1#g" config-kimi.yaml > /tmp/config-kimi-run.yaml; CFG=/tmp/config-kimi-run.yaml
fi
docker rm -f litellm-kimi >/dev/null 2>&1
docker run -d --name litellm-kimi -p 14002:4000 -e LITELLM_MASTER_KEY="$MK" -e MOONSHOT_API_KEY="$KEY" \
  -e DISABLE_ADMIN_UI=True -v "$CFG:/app/config.yaml:ro" "$IMG" --config /app/config.yaml --port 4000 >/dev/null
for i in $(seq 1 40); do curl -sf http://localhost:14002/health/liveliness >/dev/null 2>&1 && break; sleep 2; done
H=(-H "Authorization: Bearer $MK" -H 'Content-Type: application/json')
for m in kimi-native kimi-openai; do
  echo "=================== $m ==================="
  echo "-- chat 非流式 --"
  curl -s http://localhost:14002/v1/chat/completions "${H[@]}" \
    -d "{\"model\":\"$m\",\"messages\":[{\"role\":\"user\",\"content\":\"只回答一个词：你好\"}],\"max_tokens\":400}" | head -c 500; echo
  echo "-- chat 流式（末帧 usage？）--"
  curl -sN http://localhost:14002/v1/chat/completions "${H[@]}" \
    -d "{\"model\":\"$m\",\"stream\":true,\"stream_options\":{\"include_usage\":true},\"messages\":[{\"role\":\"user\",\"content\":\"只回答一个词：你好\"}],\"max_tokens\":400}" | grep -c '"usage"'
  echo "-- Responses API（桥接？）--"
  curl -s -o /tmp/kimi-resp.json -w 'http=%{http_code}\n' http://localhost:14002/v1/responses "${H[@]}" \
    -d "{\"model\":\"$m\",\"input\":\"只回答一个词：你好\",\"max_output_tokens\":400}"
  head -c 400 /tmp/kimi-resp.json; echo
  echo "-- Responses + tools（function_call？）--"
  curl -s -o /tmp/kimi-tool.json -w 'http=%{http_code}\n' http://localhost:14002/v1/responses "${H[@]}" \
    -d "{\"model\":\"$m\",\"input\":\"查一下 db.connections.active 这个指标的当前值\",\"tools\":[{\"type\":\"function\",\"name\":\"lookup_metric\",\"description\":\"查询指标当前值\",\"parameters\":{\"type\":\"object\",\"properties\":{\"name\":{\"type\":\"string\"}},\"required\":[\"name\"]}}]}"
  grep -o '"type":"function_call"' /tmp/kimi-tool.json | head -1; grep -o '"name":"lookup_metric"' /tmp/kimi-tool.json | head -1; head -c 300 /tmp/kimi-tool.json; echo
done
echo "== 日志里有没有 key 片段（前 12 字符）=="
docker logs litellm-kimi 2>&1 | grep -c "${KEY:0:12}"
docker rm -f litellm-kimi >/dev/null 2>&1
