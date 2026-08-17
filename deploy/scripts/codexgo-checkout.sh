#!/usr/bin/env bash
# spec-1.8 §8 Q1/Q7：把 codexgo 抽核分支（airush-core）按 deploy/codexgo.lock 钉住的 commit
# 放到 ../codexgo（与 go.work / agent-runtime/go.mod 的 replace 相对路径一致；本地布局同样是
# ~/airush 与 ~/codexgo 并列）。CI 每个跑 go 的 job 先执行本脚本；本地已有 ../codexgo 时只校验
# HEAD 是否等于锁定 commit（不同则告警，不改动开发者的工作树）。
# 用法：deploy/scripts/codexgo-checkout.sh [dest]   （dest 缺省 ../codexgo）
# 环境：CODEXGO_REPO 覆盖仓库地址（缺省 https://github.com/sqlrush/codexgo，公开仓，无需凭据）。
set -euo pipefail
here=$(cd "$(dirname "$0")/../.." && pwd)
lock="$here/deploy/codexgo.lock"
dest=${1:-"$here/../codexgo"}
repo=${CODEXGO_REPO:-https://github.com/sqlrush/codexgo}
commit=$(grep -E '^[0-9a-f]{40}$' "$lock" | head -1)
[ -n "$commit" ] || { echo "codexgo-checkout: $lock 里没有 40 位 commit" >&2; exit 1; }

if [ -d "$dest/.git" ]; then
  head=$(git -C "$dest" rev-parse HEAD)
  if [ "$head" != "$commit" ]; then
    echo "codexgo-checkout: WARN $dest HEAD=$head ≠ 锁定 $commit（本地工作树不改动；CI 应从空目录开始）" >&2
  else
    echo "codexgo-checkout: $dest 已在锁定 commit $commit"
  fi
  exit 0
fi

echo "codexgo-checkout: clone $repo → $dest @ $commit"
git init -q "$dest"
git -C "$dest" remote add origin "$repo"
git -C "$dest" fetch -q --depth 1 origin "$commit"
git -C "$dest" checkout -q --detach FETCH_HEAD
echo "codexgo-checkout: done ($(git -C "$dest" rev-parse --short HEAD))"
