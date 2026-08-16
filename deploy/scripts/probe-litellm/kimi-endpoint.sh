#!/bin/zsh
# 直连判定 key 归属与可用模型（不经 LiteLLM）。key 只从 ~/.airush/kimi.key 读。
set -u
KEY=$(cat ~/.airush/kimi.key)
for base in https://api.moonshot.cn/v1 https://api.moonshot.ai/v1 https://api.kimi.com/coding/v1 https://api.kimi.com/v1; do
  code=$(curl -s -o /tmp/kimi-models.json -w '%{http_code}' -m 20 "$base/models" -H "Authorization: Bearer $KEY")
  echo "== $base/models → http=$code"
  [ "$code" = "200" ] && grep -o '"id":"[^"]*"' /tmp/kimi-models.json | head -20 || head -c 160 /tmp/kimi-models.json; echo
done
echo "== coding/v1 chat（模型名试 kimi-k3 / kimi-for-coding）=="
for m in kimi-k3 kimi-for-coding; do
  code=$(curl -s -o /tmp/kimi-chat.json -w '%{http_code}' -m 60 https://api.kimi.com/coding/v1/chat/completions \
    -H "Authorization: Bearer $KEY" -H 'Content-Type: application/json' \
    -d "{\"model\":\"$m\",\"messages\":[{\"role\":\"user\",\"content\":\"只回答一个词：你好\"}],\"max_tokens\":20}")
  echo "-- $m → http=$code"; head -c 400 /tmp/kimi-chat.json; echo
done
