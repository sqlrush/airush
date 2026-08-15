#!/bin/zsh
set -eu
export PATH="/opt/homebrew/bin:/usr/local/bin:$PATH"
export GOFLAGS=-mod=readonly
cd /Users/sqlrush/airush/libs/metrics
gofmt -l . && go vet ./... && go test -race -count=1 ./... 2>&1 | tail -30
