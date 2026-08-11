#!/usr/bin/env bash
# spec-0.4 D1：Go 覆盖率闸门。按模块统计（spec-0.4 §2.1 剔除口径），报告恒出；
# COVER_ENFORCE=1 时阈值不足退出非零。
# 口径修订（spec-1.1，user approve 2026-08-10）：单测 profile 与集成 profile
# （bin/cover/integration/，若存在）按语句块去重合并——DB 重模块的 SQL 粘合/装配层
# 正确性证据在集成测试，合并口径下"未被任何测试执行的代码"才是真缺口。
set -euo pipefail
cd "$(dirname "$0")/../.."

THRESHOLD=80
fail=0

echo "== Go coverage (threshold ${THRESHOLD}%, enforce=${COVER_ENFORCE:-0}, unit∪integration) =="
for f in bin/cover/*.out; do
  [ -e "$f" ] || { echo "no coverage profiles found"; exit 1; }
  m=$(basename "$f" .out)
  if [ "$m" = "testkit" ]; then
    printf "  %-16s %8s  (测试基建，逻辑由集成态覆盖，豁免——spec-0.4 修订)\n" "$m" "n/a"
    continue
  fi
  profiles=("$f")
  merged=""
  if [ -e "bin/cover/integration/$m.out" ]; then
    profiles+=("bin/cover/integration/$m.out")
    merged="+integration"
  fi
  pct=$(awk '
    /^mode:/ { next }
    {
      if ($1 ~ /cmd\/[a-z-]+\/main\.go:/) next        # 装配层
      if ($1 ~ /\/gen\/main\.go:/) next               # 生成器工具（装配层同类）
      if ($1 ~ /_gen\.go:/ || $1 ~ /\.pb\.go:/) next  # 生成代码
      if ($1 ~ /internal\/codexcore\//) next          # vendored
      key = $1
      stmts[key] = $2
      if ($3 > 0) cov[key] = 1
    }
    END {
      for (k in stmts) { total += stmts[k]; if (cov[k]) covered += stmts[k] }
      if (total == 0) print "-1"; else printf "%.1f", covered * 100 / total
    }
  ' "${profiles[@]}")
  if [ "$pct" = "-1" ]; then
    printf "  %-16s %8s  (无纳入统计的语句，视为通过)\n" "$m" "n/a"
    continue
  fi
  ok=$(awk -v p="$pct" -v t="$THRESHOLD" 'BEGIN { print (p >= t) ? 1 : 0 }')
  if [ "$ok" = "1" ]; then
    printf "  %-16s %7s%%  PASS %s\n" "$m" "$pct" "$merged"
  else
    printf "  %-16s %7s%%  BELOW %s%% %s\n" "$m" "$pct" "$THRESHOLD" "$merged"
    if [ "${COVER_ENFORCE:-0}" = "1" ]; then fail=1; fi
  fi
done

exit $fail
