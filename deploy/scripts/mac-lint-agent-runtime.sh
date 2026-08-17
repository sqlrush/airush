#!/bin/zsh
# spec-1.8：agent-runtime lint。CI 口径不带 integration tag（mac-lint-go.sh），本脚本额外带上 tag
# 把集成用例文件也过一遍 lint（它们的量比单测大）。用仓库钉版 golangci-lint。
set -eu
export PATH="/opt/homebrew/bin:/usr/local/bin:$PATH"
export GOFLAGS=-mod=readonly
cd /Users/sqlrush/airush
GOLANGCI=$(ls bin/tools/golangci-lint-* 2>/dev/null | head -1)
[ -n "$GOLANGCI" ] || { echo "golangci-lint not installed; run 'make lint-go' once" >&2; exit 1; }
GOLANGCI="$PWD/$GOLANGCI"
echo "==> lint agent-runtime (integration tag)"
(cd agent-runtime && "$GOLANGCI" run --build-tags integration ./...)
