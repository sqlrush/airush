#!/usr/bin/env bash
# spec-0.8 T8：错误码不可删守护——相对基线分支，registry 中已存在的码
# 被删除即红（弃用走 deprecated=true）。
set -euo pipefail
cd "$(dirname "$0")/../.."

base="${1:-origin/main}"
git rev-parse --verify -q "$base" >/dev/null || { echo "base $base 不可用（需先 fetch）"; exit 1; }
git cat-file -e "$base:proto/errors.json" 2>/dev/null || { echo "base 无 registry，跳过"; exit 0; }

removed=$(comm -23 \
  <(git show "$base:proto/errors.json" | grep -oE '"AR_[A-Z0-9_]+"' | sort -u) \
  <(grep -oE '"AR_[A-Z0-9_]+"' proto/errors.json | sort -u))

if [ -n "$removed" ]; then
  echo "ERROR: 错误码禁止删除（客户端兼容契约，弃用置 deprecated=true）："
  echo "$removed"
  exit 1
fi
echo "error codes immutability OK (base: $base)"
