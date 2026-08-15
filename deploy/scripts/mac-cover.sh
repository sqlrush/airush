#!/bin/zsh
# Merged coverage gate (unit ∪ integration) on the Mac host.
# Clears the build cache first: stale instrumented objects keep old line numbers
# and inflate the uncovered denominator with phantom blocks (2026-08-12 trap).
set -eu
export PATH="/opt/homebrew/bin:/usr/local/bin:$PATH"
export GOFLAGS=-mod=readonly
export TESTCONTAINERS_RYUK_DISABLED=${TESTCONTAINERS_RYUK_DISABLED:-true}
export AIRUSH_OPENGAUSS_HOST=${AIRUSH_OPENGAUSS_HOST:-127.0.0.1}
export AIRUSH_OPENGAUSS_PORT=${AIRUSH_OPENGAUSS_PORT:-5433}
export AIRUSH_OPENGAUSS_PASSWORD=${AIRUSH_OPENGAUSS_PASSWORD:-Root@1234}
cd /Users/sqlrush/airush

rm -rf bin/cover
go clean -testcache
go clean -cache
make integration-test-go
make cover-go
