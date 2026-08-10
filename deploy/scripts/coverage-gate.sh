#!/usr/bin/env bash
# spec-0.4 D1：Go 覆盖率闸门。按模块统计（spec-0.4 §2.1 剔除口径），报告恒出；
# COVER_ENFORCE=1 时阈值不足退出非零（阻断自 spec-1.1 合并起在 CI 打开）。
set -euo pipefail
cd "$(dirname "$0")/../.."

THRESHOLD=80
fail=0

echo "== Go coverage (threshold ${THRESHOLD}%, enforce=${COVER_ENFORCE:-0}) =="
for f in bin/cover/*.out; do
  [ -e "$f" ] || { echo "no coverage profiles found"; exit 1; }
  m=$(basename "$f" .out)
  if [ "$m" = "testkit" ]; then
    printf "  %-16s %8s  (测试基建，逻辑由集成态覆盖，豁免——spec-0.4 修订)\n" "$m" "n/a"
    continue
  fi
  pct=$(awk '
    NR > 1 {
      if ($1 ~ /cmd\/[a-z-]+\/main\.go:/) next        # 装配层
      if ($1 ~ /\/gen\/main\.go:/) next               # 生成器工具（装配层同类）
      if ($1 ~ /_gen\.go:/ || $1 ~ /\.pb\.go:/) next  # 生成代码
      if ($1 ~ /internal\/codexcore\//) next          # vendored
      total += $2; if ($3 > 0) covered += $2
    }
    END { if (total == 0) print "-1"; else printf "%.1f", covered * 100 / total }
  ' "$f")
  if [ "$pct" = "-1" ]; then
    printf "  %-16s %8s  (无纳入统计的语句，视为通过)\n" "$m" "n/a"
    continue
  fi
  ok=$(awk -v p="$pct" -v t="$THRESHOLD" 'BEGIN { print (p >= t) ? 1 : 0 }')
  if [ "$ok" = "1" ]; then
    printf "  %-16s %7s%%  PASS\n" "$m" "$pct"
  else
    printf "  %-16s %7s%%  BELOW %s%%\n" "$m" "$pct" "$THRESHOLD"
    if [ "${COVER_ENFORCE:-0}" = "1" ]; then fail=1; fi
  fi
done

exit $fail
