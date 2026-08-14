#!/bin/zsh
# spec-1.5 全量回归：四个模块的单测 + 受影响的集成用例。
set -eu
export PATH="/opt/homebrew/bin:/usr/local/bin:$PATH"
export GOFLAGS=-mod=readonly
ROOT=/Users/sqlrush/airush
fail=0
for m in libs/metrics gateway connector console; do
  echo "=== $m 单测 ==="
  (cd "$ROOT/$m" && go test -race -count=1 ./... 2>&1 | grep -E "^(FAIL|---|ok)" ) || fail=1
done
echo "=== gateway 集成 ==="
(cd "$ROOT/gateway" && go test -tags=integration -count=1 ./... 2>&1 | grep -E "^(ok|FAIL|---)") || fail=1
echo "=== console 集成 ==="
(cd "$ROOT/console" && go test -tags=integration -count=1 ./... 2>&1 | grep -E "^(ok|FAIL|---)") || fail=1
[ "$fail" = 0 ] && echo "SPEC-1.5 REGRESSION OK" || { echo "SPEC-1.5 REGRESSION FAILED"; exit 1; }
