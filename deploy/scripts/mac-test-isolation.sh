#!/bin/zsh
# 跑 spec-1.5 的隔离集成用例（T7-T10）。宿主机执行——容器与 Go 工具链都在 Mac 上。
set -eu
export PATH="/opt/homebrew/bin:/usr/local/bin:$PATH"
export GOFLAGS=-mod=readonly
cd /Users/sqlrush/airush/console
go test -tags=integration -run 'TestCollected' -v -count=1 ./internal/dbmigrate/ 2>&1 | tail -60
