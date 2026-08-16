#!/bin/zsh
# spec-1.7 排障：LLM Pod 两个容器的状态与日志。
set -u
export PATH="/opt/homebrew/bin:/usr/local/bin:$PATH"
K=(kubectl --context kind-airush-dev)
POD=$("${K[@]}" get pod -l app.kubernetes.io/name=llm -o jsonpath='{.items[0].metadata.name}')
echo "pod=$POD"
"${K[@]}" get pod "$POD" -o jsonpath='{range .status.containerStatuses[*]}{.name}: ready={.ready} restarts={.restartCount} waiting={.state.waiting.reason} term={.lastState.terminated.reason}/{.lastState.terminated.exitCode}{"\n"}{end}'
echo "== litellm logs =="
"${K[@]}" logs "$POD" -c litellm --tail=${1:-30} 2>&1 | tail -${1:-30}
echo "== litellm previous =="
"${K[@]}" logs "$POD" -c litellm --previous --tail=20 2>&1 | tail -20
echo "== mockllm logs =="
"${K[@]}" logs "$POD" -c mockllm --tail=5 2>&1 | tail -5
echo "== events =="
"${K[@]}" get events --field-selector involvedObject.name="$POD" --sort-by=.lastTimestamp 2>&1 | tail -8
