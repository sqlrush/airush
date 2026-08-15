#!/bin/zsh
# Format Go modules with the repo-pinned golangci-lint (gofumpt + gci) on the
# Mac host. Args: module dirs relative to the repo root (default: libs/metrics).
set -eu
export PATH="/opt/homebrew/bin:/usr/local/bin:$PATH"
cd /Users/sqlrush/airush

GOLANGCI=$(ls bin/tools/golangci-lint-* 2>/dev/null | head -1)
if [ -z "$GOLANGCI" ]; then
  echo "golangci-lint not installed; run 'make lint-go' once" >&2
  exit 1
fi
GOLANGCI="$PWD/$GOLANGCI"

mods=${@:-libs/metrics}
for m in ${=mods}; do
  echo "==> fmt $m"
  (cd "$m" && GOFLAGS= "$GOLANGCI" fmt ./...)
done
