#!/bin/zsh
# 带 integration tag 跑 gofmt + vet。
# 默认 `go vet ./...` 不看 //go:build integration 文件，集成用例的编译错误会一直藏着，
# 直到某次真跑集成测试才炸出来——这个脚本把它提前。
set -eu
export PATH="/opt/homebrew/bin:/usr/local/bin:$PATH"
export GOFLAGS=-mod=readonly
ROOT=/Users/sqlrush/airush
for m in console gateway connector libs/metrics; do
  echo "=== $m ==="
  (cd "$ROOT/$m" && gofmt -w . && go vet -tags=integration ./...) || exit 1
done
echo "VET(integration) OK"
