#!/bin/zsh
set -eu
export PATH="/opt/homebrew/bin:/usr/local/bin:$PATH"
export GOFLAGS=-mod=readonly
cd /Users/sqlrush/airush
for m in console gateway connector libs/metrics; do
  echo "=== $m ==="
  (cd "$m" && gofmt -l . && go build ./... && go vet ./...) || exit 1
done
echo "ALL BUILD OK"
