#!/bin/zsh
# directconn integration tests on the Mac host. openGauss cases need the og5
# container (see og5-query.sh); they skip when the env vars are unset.
set -eu
export PATH="/opt/homebrew/bin:/usr/local/bin:$PATH"
export GOFLAGS=-mod=readonly
export TESTCONTAINERS_RYUK_DISABLED=${TESTCONTAINERS_RYUK_DISABLED:-true}
export AIRUSH_OPENGAUSS_HOST=${AIRUSH_OPENGAUSS_HOST:-127.0.0.1}
export AIRUSH_OPENGAUSS_PORT=${AIRUSH_OPENGAUSS_PORT:-5433}
export AIRUSH_OPENGAUSS_PASSWORD=${AIRUSH_OPENGAUSS_PASSWORD:-Root@1234}
cd /Users/sqlrush/airush/console
go test -tags integration ./internal/directconn/... "$@"
