#!/bin/zsh
# Run libs/metrics tests on the Mac host (the only machine that may build Go
# artifacts in the shared /Users/sqlrush tree).
set -eu
export PATH="/opt/homebrew/bin:/usr/local/bin:$PATH"
export GOFLAGS=-mod=readonly
cd /Users/sqlrush/airush/libs/metrics
gofmt -l .
go vet ./...
go test -race ./... "$@"
