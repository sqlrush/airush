#!/bin/zsh
# spec-1.7 起草期实测：LiteLLM 无状态形态的行为。宿主机只跑我们自己的 mock 二进制，
# LiteLLM 在容器里。输出人工阅读，结论写进 spec。
set -u
export PATH="/opt/homebrew/bin:/usr/local/bin:$PATH"
cd /Users/sqlrush/airush/deploy/scripts/probe-litellm
IMG=ghcr.io/berriai/litellm:main-stable

echo "== 0. LiteLLM 版本 =="
docker run --rm "$IMG" --version 2>&1 | tail -1

echo "== 1. 起 mock 供应商（宿主机 :18099）=="
GOWORK=off go run . > mock.log 2>&1 &
MOCK=$!
sleep 2

echo "== 2. 起 LiteLLM（无 DB，端口 14000）=="
docker rm -f litellm-probe >/dev/null 2>&1
docker run -d --name litellm-probe -p 14000:4000 \
  -v "$PWD/config.yaml:/app/config.yaml:ro" \
  "$IMG" --config /app/config.yaml --port 4000 >/dev/null
for i in $(seq 1 40); do
  curl -sf http://localhost:14000/health/liveliness >/dev/null 2>&1 && break
  sleep 2
done
echo "liveliness: $(curl -s http://localhost:14000/health/liveliness)"
echo "readiness:  $(curl -s http://localhost:14000/health/readiness | head -c 300)"

echo "== 3. /v1/models（需 master key）=="
echo "no-key: $(curl -s -o /dev/null -w '%{http_code}' http://localhost:14000/v1/models)"
curl -s http://localhost:14000/v1/models -H 'Authorization: Bearer sk-probe-master' | head -c 400; echo

echo "== 4. chat completions 非流式 =="
curl -s http://localhost:14000/v1/chat/completions -H 'Authorization: Bearer sk-probe-master' \
  -H 'Content-Type: application/json' \
  -d '{"model":"chat-default","messages":[{"role":"user","content":"hi"}]}' | head -c 600; echo

echo "== 5. chat completions 流式（要 usage）=="
curl -sN http://localhost:14000/v1/chat/completions -H 'Authorization: Bearer sk-probe-master' \
  -H 'Content-Type: application/json' \
  -d '{"model":"chat-default","stream":true,"stream_options":{"include_usage":true},"messages":[{"role":"user","content":"hi"}]}' | grep -c "usage"

echo "== 6. Responses API（codexgo 默认线协议）→ 后端是 chat 供应商，看会不会转换 =="
curl -s -o /tmp/resp.json -w 'http=%{http_code}\n' http://localhost:14000/v1/responses -H 'Authorization: Bearer sk-probe-master' \
  -H 'Content-Type: application/json' \
  -d '{"model":"chat-default","input":"hi"}'
head -c 500 /tmp/resp.json; echo

echo "== 7. /metrics（Prometheus）=="
curl -s -o /dev/null -w 'http=%{http_code}\n' http://localhost:14000/metrics -H 'Authorization: Bearer sk-probe-master'
curl -s http://localhost:14000/metrics -H 'Authorization: Bearer sk-probe-master' | head -c 300; echo

echo "== 8. 未知模型名 → 错误形态 =="
curl -s http://localhost:14000/v1/chat/completions -H 'Authorization: Bearer sk-probe-master' \
  -H 'Content-Type: application/json' \
  -d '{"model":"nope","messages":[{"role":"user","content":"hi"}]}' | head -c 300; echo

echo "== 9. LiteLLM 默认日志里有没有 prompt 内容（AD-3 关注）=="
docker logs litellm-probe 2>&1 | grep -c '"content"\|hi' ; docker logs litellm-probe 2>&1 | tail -15

echo "== 10. mock 侧看到的请求（透传了什么）=="
cat mock.log

kill $MOCK 2>/dev/null
docker rm -f litellm-probe >/dev/null 2>&1
