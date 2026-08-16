#!/bin/zsh
# libs/llm 单测（race）；Args: 额外 go test 参数。
set -eu
export PATH="/opt/homebrew/bin:/usr/local/bin:$PATH"
export GOFLAGS=-mod=readonly
cd /Users/sqlrush/airush/libs/llm
gofmt -w . && go vet ./... && go test -race -count=1 "$@" ./... 2>&1 | tail -40
