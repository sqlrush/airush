#!/bin/zsh
# spec-1.8：agent-runtime 合并覆盖率（unit ∪ integration，-race）。先清缓存（陈旧插桩陷阱）。
set -eu
export PATH="/opt/homebrew/bin:/usr/local/bin:$PATH"
export GOFLAGS=-mod=readonly
export TESTCONTAINERS_RYUK_DISABLED=${TESTCONTAINERS_RYUK_DISABLED:-true}
cd /Users/sqlrush/airush/agent-runtime
go clean -testcache
mkdir -p /tmp/airush-cover
go test -race -tags integration -count=1 -coverprofile=/tmp/airush-cover/agent-runtime.cov -coverpkg=./... ./... 2>&1 | grep -v "no test files"
go tool cover -func=/tmp/airush-cover/agent-runtime.cov | tail -1
go tool cover -func=/tmp/airush-cover/agent-runtime.cov | awk '$3+0 < 60 && $2 != "total:"' | sort -k3 -n | head -25
