#!/usr/bin/env bash
# spec-0.6 T8（实施修订版）：迁移不可变检查——相对基线分支，
# 已存在迁移文件被修改(M)/删除(D)/改名(R) 即红；新增(A) 放行。
# golang-migrate 无内建校验和，由本检查在 CI 强制（spec-0.6 §3）。
set -euo pipefail
cd "$(dirname "$0")/../.."

base="${1:-origin/main}"
git rev-parse --verify -q "$base" >/dev/null || { echo "base $base 不可用（需先 fetch）"; exit 1; }

bad=$(git diff --name-status "$base" -- 'console/migrations/*.sql' | grep -Ev '^A' || true)
if [ -n "$bad" ]; then
  echo "ERROR: 已合并的迁移文件不可修改/删除（修正走新迁移，spec-0.6 §2.1）："
  echo "$bad"
  exit 1
fi
echo "migration immutability OK (base: $base)"
