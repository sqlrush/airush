#!/bin/zsh
set -u
export PATH="/opt/homebrew/bin:/usr/local/bin:$PATH"
cd /Users/sqlrush/airush/deploy/scripts/probe-litellm
IMG=ghcr.io/berriai/litellm:main-stable

echo "== 0. 版本 =="
docker run --rm --entrypoint /bin/sh "$IMG" -c '/app/.venv/bin/python -c "import litellm,sys;print(litellm.__version__ if hasattr(litellm,\"__version__\") else \"n/a\")"; /app/.venv/bin/pip show litellm 2>/dev/null | grep -i ^version' 2>&1 | tail -2

GOWORK=off go run . > mock2.log 2>&1 &
MOCK=$!
sleep 2
docker rm -f litellm-probe >/dev/null 2>&1
docker run -d --name litellm-probe -p 14000:4000 -v "$PWD/config2.yaml:/app/config.yaml:ro" \
  "$IMG" --config /app/config.yaml --port 4000 >/dev/null
for i in $(seq 1 40); do curl -sf http://localhost:14000/health/liveliness >/dev/null 2>&1 && break; sleep 2; done

echo "== 1. Responses API → deepseek/ 前缀（chat-only 供应商）=="
curl -s -o /tmp/r1.json -w 'http=%{http_code}\n' http://localhost:14000/v1/responses -H 'Authorization: Bearer sk-probe-master' \
  -H 'Content-Type: application/json' -d '{"model":"chat-deepseek","input":"hi"}'
head -c 400 /tmp/r1.json; echo
echo "== 2. Responses API → openai/ 前缀（对照）=="
curl -s -o /tmp/r2.json -w 'http=%{http_code}\n' http://localhost:14000/v1/responses -H 'Authorization: Bearer sk-probe-master' \
  -H 'Content-Type: application/json' -d '{"model":"chat-openai","input":"hi"}'
head -c 200 /tmp/r2.json; echo
echo "== 3. chat 非流式（成功路径，用于看日志）=="
curl -s http://localhost:14000/v1/chat/completions -H 'Authorization: Bearer sk-probe-master' \
  -H 'Content-Type: application/json' -d '{"model":"chat-deepseek","messages":[{"role":"user","content":"SECRET-CANARY-42"}]}' | head -c 200; echo
echo "== 4. /metrics（prometheus 回调已开）=="
curl -s -o /tmp/m.txt -w 'http=%{http_code}\n' http://localhost:14000/metrics -H 'Authorization: Bearer sk-probe-master'
head -c 400 /tmp/m.txt; echo
echo "== 5. 日志里 canary 出现次数（成功路径是否记录 prompt 内容）=="
docker logs litellm-probe 2>&1 | grep -c "SECRET-CANARY-42"
echo "== 5b. 启动日志里与 prometheus/enterprise 相关的提示 =="
docker logs litellm-probe 2>&1 | grep -iE "prometheus|enterprise|premium" | head -5
echo "== 6. mock 侧 =="
cat mock2.log
kill $MOCK 2>/dev/null; docker rm -f litellm-probe >/dev/null 2>&1
