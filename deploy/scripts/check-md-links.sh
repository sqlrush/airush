#!/usr/bin/env bash
# spec-0.3：markdown 相对链接检查（docs 组唯一 CI 步骤，轻量不占额度）。
# 只校验仓库内相对路径链接的目标存在性；外链不访问（避免网络抖动误报）。
set -euo pipefail
cd "$(dirname "$0")/../.."

fail=0
while IFS= read -r -d '' f; do
  dir=$(dirname "$f")
  links=$(grep -oE '\]\([^)#[:space:]]+' "$f" | sed 's/^](//' || true)
  [ -z "$links" ] && continue
  while IFS= read -r link; do
    case "$link" in
      http://* | https://* | mailto:* | /*) continue ;;
      \"* | \'*) continue ;; # 代码片段中的 ]("...") 形态，非 markdown 链接
    esac
    if [ ! -e "$dir/$link" ]; then
      echo "BROKEN: $f -> $link"
      fail=1
    fi
  done <<<"$links"
done < <(find . -name '*.md' \
  -not -path './node_modules/*' -not -path './.venv/*' \
  -not -path './frontend/node_modules/*' -print0)

exit $fail
