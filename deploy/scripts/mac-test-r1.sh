#!/bin/zsh
# spec-1.5 R1 基准：经隔离视图写入 vs 直插基表，退化须 ≤30%。
set -eu
export PATH="/opt/homebrew/bin:/usr/local/bin:$PATH"
export GOFLAGS=-mod=readonly
cd /Users/sqlrush/airush/console
gofmt -w ./internal/tsstore/
go test -tags=integration -count=1 -v -run TestViewWriteOverheadWithinBudget \
  ./internal/tsstore/ 2>&1 | grep -vE "^20[0-9]{2}/|🐳|✅|⏳|🔔|🚫|Shell not found"
