#!/usr/bin/env bash
# spec-0.6 D2：生成下一编号的迁移文件对（make migrate-new name=xxx）。
set -euo pipefail
cd "$(dirname "$0")/../.."

name="${1:?用法: migrate-new.sh <snake_case_name>}"
if ! [[ "$name" =~ ^[a-z0-9_]+$ ]]; then
  echo "ERROR: 名称必须 snake_case（小写字母/数字/下划线）: $name"
  exit 2
fi

dir=console/migrations
last=$(find "$dir" -name '[0-9][0-9][0-9][0-9]_*.up.sql' | sed 's/.*\/\([0-9]*\)_.*/\1/' | sort -n | tail -1)
next=$(printf "%04d" $((10#${last:-0} + 1)))

up="$dir/${next}_${name}.up.sql"
down="$dir/${next}_${name}.down.sql"

cat > "$up" <<EOF
-- ${next}_${name}
-- 单一意图；租户表必须使用 spec-0.6 §2.2 模板四要素；
-- 系统表需头部注释: -- SYSTEM TABLE (no tenant scope): <理由>
-- 不可逆迁移在此标注: -- IRREVERSIBLE
EOF

cat > "$down" <<EOF
-- ${next}_${name} 回滚：仅结构回滚；不可逆时改为 RAISE EXCEPTION 'irreversible'。
EOF

echo "created: $up"
echo "created: $down"
