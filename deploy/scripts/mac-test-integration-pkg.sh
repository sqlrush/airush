#!/bin/zsh
# 单包集成测试（testcontainers）在 Mac 宿主机跑。用法：mac-test-integration-pkg.sh <module> <pkg-pattern> [-run Regexp]
# 例：mac-test-integration-pkg.sh console ./internal/dbmigrate/ -run TestAgentThreadsMigration
set -eu
export PATH="/opt/homebrew/bin:/usr/local/bin:$PATH"
export GOFLAGS=-mod=readonly
export TESTCONTAINERS_RYUK_DISABLED=${TESTCONTAINERS_RYUK_DISABLED:-true}
cd /Users/sqlrush/airush
mod=$1; shift
pkg=$1; shift
(cd "$mod" && go test -tags integration -count=1 "$pkg" "$@")
