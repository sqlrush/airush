#!/bin/zsh
# 彻底重建 kind 开发栈后跑 dev-verify（spec-1.5：PG 镜像换成 TimescaleDB，
# 沿用旧 PVC 会带着 postgres:16.6 初始化的数据目录，collation 警告即其征兆。
# 验"从零"就该真从零）。
set -eu
export PATH="/opt/homebrew/bin:/usr/local/bin:$PATH"
export GOFLAGS=-mod=readonly
cd /Users/sqlrush/airush
make dev-down || true
make dev-up
bash deploy/scripts/dev-verify.sh
