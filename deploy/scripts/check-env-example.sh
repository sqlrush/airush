#!/usr/bin/env bash
# spec-0.7 T8：.env.example 与 appConfig 字段一致性（需先 make build-go）。
set -euo pipefail
cd "$(dirname "$0")/../.."

fail=0
for m in console connector gateway agent-runtime; do
  [ -x "bin/$m" ] || { echo "ERROR: bin/$m 不存在，请先 make build-go"; exit 2; }
  code=$(./bin/$m --config-keys | sort)
  example=$(grep -oE '^AIRUSH_[A-Z_]+' "$m/.env.example" | sort)
  if ! diff <(echo "$code") <(echo "$example") >/dev/null; then
    echo "ERROR: $m/.env.example 与代码配置面不一致："
    diff <(echo "$code") <(echo "$example") | sed 's/^/  /' || true
    fail=1
  fi
done
[ $fail -eq 0 ] && echo "env example consistency OK"
exit $fail
