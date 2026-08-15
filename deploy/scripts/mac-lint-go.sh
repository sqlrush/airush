#!/bin/zsh
# 用仓库钉版的 golangci-lint 跑 lint（与 CI ci/lint 同配置、同 build tag 口径——
# 不带 integration tag；集成用例的编译检查归 mac-vet-integration.sh）。Args: module dirs.
# 推 PR 前跑一次，别让 CI 当第一道 lint——一次往返十几分钟。
set -eu
export PATH="/opt/homebrew/bin:/usr/local/bin:$PATH"
cd /Users/sqlrush/airush

GOLANGCI=$(ls bin/tools/golangci-lint-* 2>/dev/null | head -1)
if [ -z "$GOLANGCI" ]; then
  echo "golangci-lint not installed; run 'make lint-go' once" >&2
  exit 1
fi
GOLANGCI="$PWD/$GOLANGCI"

mods=${@:-"console gateway connector libs/metrics libs/apierror"}
for m in ${=mods}; do
  echo "==> lint $m"
  (cd "$m" && GOFLAGS=-mod=readonly "$GOLANGCI" run ./...)
done
echo "LINT OK"
