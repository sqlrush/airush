#!/bin/zsh
set -eu
export PATH="/opt/homebrew/bin:/usr/local/bin:$PATH"
export GOFLAGS=-mod=readonly
cd /Users/sqlrush/airush/console
gofmt -w ./internal/tsstore/
go test -tags=integration -count=1 -v ./internal/tsstore/ 2>&1 | grep -vE "^20[0-9]{2}/|🐳|✅|⏳|🔔|🚫|Shell not found" | tail -40
