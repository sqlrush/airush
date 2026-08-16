#!/bin/zsh
set -u
export PATH="/opt/homebrew/bin:/usr/local/bin:$PATH"
K=(kubectl --context kind-airush-dev)
"${K[@]}" get jobs 2>&1 | tail -3
POD=$("${K[@]}" get pods -l app.kubernetes.io/name=migrate --sort-by=.metadata.creationTimestamp -o jsonpath='{.items[-1:].metadata.name}' 2>/dev/null)
echo "pod=$POD"
[ -n "$POD" ] && "${K[@]}" logs "$POD" -c migrate --tail=40 2>&1 | tail -40
