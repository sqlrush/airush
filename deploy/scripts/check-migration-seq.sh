#!/usr/bin/env bash
# spec-0.6 T7：迁移编号检查——重号/断号/缺 down 任一即红。
set -euo pipefail
cd "$(dirname "$0")/../.."
dir=console/migrations

nums=$(find "$dir" -name '[0-9][0-9][0-9][0-9]_*.up.sql' | sed 's/.*\/\([0-9]*\)_.*/\1/' | sort -n)
[ -n "$nums" ] || { echo "no migrations found in $dir"; exit 1; }

if [ "$(echo "$nums" | wc -l)" -ne "$(echo "$nums" | sort -u | wc -l)" ]; then
  echo "ERROR: 迁移编号重复"
  echo "$nums" | uniq -d
  exit 1
fi

expect=1
for n in $nums; do
  if [ "$((10#$n))" -ne "$expect" ]; then
    echo "ERROR: 迁移编号断号——期望 $(printf %04d $expect)，实际 $n"
    exit 1
  fi
  expect=$((expect + 1))
done

for up in "$dir"/[0-9]*_*.up.sql; do
  down="${up%.up.sql}.down.sql"
  [ -f "$down" ] || { echo "ERROR: 缺少 down 迁移: $down"; exit 1; }
done

echo "migration seq OK ($(echo "$nums" | wc -l | tr -d ' ') migrations)"
