#!/bin/zsh
# Build + vet every Go module on the Mac host. Args: module dirs (default: all).
set -eu
export PATH="/opt/homebrew/bin:/usr/local/bin:$PATH"
export GOFLAGS=-mod=readonly
cd /Users/sqlrush/airush

mods=${@:-"libs/apierror libs/obs libs/metrics console gateway connector testkit"}
for m in ${=mods}; do
  echo "==> build $m"
  (cd "$m" && go build ./... && go vet ./...)
done
echo "all built"
