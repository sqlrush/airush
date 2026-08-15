#!/bin/zsh
# Integration tests (testcontainers) on the Mac host. Args: module dirs.
set -eu
export PATH="/opt/homebrew/bin:/usr/local/bin:$PATH"
export GOFLAGS=-mod=readonly
export TESTCONTAINERS_RYUK_DISABLED=${TESTCONTAINERS_RYUK_DISABLED:-true}
export AIRUSH_OPENGAUSS_HOST=${AIRUSH_OPENGAUSS_HOST:-127.0.0.1}
export AIRUSH_OPENGAUSS_PORT=${AIRUSH_OPENGAUSS_PORT:-5433}
export AIRUSH_OPENGAUSS_PASSWORD=${AIRUSH_OPENGAUSS_PASSWORD:-Root@1234}
cd /Users/sqlrush/airush

mods=${@:-"console gateway connector"}
for m in ${=mods}; do
  echo "==> integration $m"
  (cd "$m" && go test -tags integration ./...)
done
