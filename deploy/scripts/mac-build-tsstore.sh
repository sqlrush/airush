#!/bin/zsh
set -eu
export PATH="/opt/homebrew/bin:/usr/local/bin:$PATH"
export GOFLAGS=-mod=readonly
cd /Users/sqlrush/airush/console
gofmt -w ./internal/tsstore/ && go build ./... && go vet ./internal/tsstore/ && echo "BUILD OK"
