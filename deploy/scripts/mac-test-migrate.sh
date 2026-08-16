#!/bin/zsh
# 只跑 dbmigrate 集成用例（Args: -run 正则，默认全部）。
set -eu
export PATH="/opt/homebrew/bin:/usr/local/bin:$PATH"
export GOFLAGS=-mod=readonly
cd /Users/sqlrush/airush/console
go test -tags=integration -count=1 -v -run "${1:-.}" ./internal/dbmigrate/ 2>&1 | grep -vE "^20[0-9]{2}/|🐳|✅|⏳|🔔|🚫|Shell not found|Server Version|API Version|Operating System|Total Memory|Testcontainers for Go|Resolved Docker|Test SessionID|Test ProcessID"
