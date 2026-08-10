#!/usr/bin/env bash
# spec-0.11 D4：发布准备（人触发）。校验 → CHANGELOG 落段 → 输出后续步骤。
# 用法: release-prep.sh <new-version>   （如 0.1.0 或 0.1.0-rc.1）
set -euo pipefail
cd "$(dirname "$0")/../.."

NEW="${1:?用法: release-prep.sh <semver，不带 v 前缀>}"
[[ "$NEW" =~ ^[0-9]+\.[0-9]+\.[0-9]+(-rc\.[0-9]+)?$ ]] || { echo "ERROR: 非法版本号 $NEW"; exit 2; }

# T4：Unreleased 段必须有实质内容
unreleased=$(awk '/^## \[Unreleased\]/{grab=1;next} grab && /^## \[/{exit} grab{print}' CHANGELOG.md \
  | grep -vE '^\s*$|^###' || true)
[ -n "$unreleased" ] || { echo "ERROR: Unreleased 段为空，拒绝空发布（spec-0.11 §3）"; exit 1; }

# T7：版本必须严格大于最近 tag（rc 与正式同序比较，sort -V 语义）
last=$(git tag -l 'v*' --sort=-v:refname | head -1 | sed 's/^v//')
if [ -n "$last" ]; then
  top=$(printf '%s\n%s\n' "$last" "$NEW" | sort -V | tail -1)
  { [ "$top" = "$NEW" ] && [ "$NEW" != "$last" ]; } \
    || { echo "ERROR: 版本 $NEW 未递增（最近 tag v$last）"; exit 1; }
fi

# rc 不落段（正式发布时一次性落段）；正式版本把 Unreleased 落为版本段
if [[ "$NEW" != *-rc.* ]]; then
  today=$(date +%Y-%m-%d)
  awk -v ver="$NEW" -v d="$today" '
    /^## \[Unreleased\]/ { print; print ""; print "## [" ver "] - " d; next }
    { print }
  ' CHANGELOG.md > CHANGELOG.md.tmp && mv CHANGELOG.md.tmp CHANGELOG.md
  echo "CHANGELOG：Unreleased 已落段为 [$NEW] - $today（请 review diff）"
fi

cat <<EOF

后续步骤（人工确认后执行）：
  1. git diff 复核 CHANGELOG（正式版本）
  2. 经 PR 合并 CHANGELOG 变更（rc 跳过）
  3. git tag v$NEW <main上的目标commit> && git push origin v$NEW
  4. release workflow 自动完成：全绿校验 → 镜像 v$NEW → chart → GitHub Release
EOF
