#!/bin/zsh
# helm lint + template 渲染检查（Mac 侧 helm）。渲染物顺带 grep 明文 key（spec-1.7 T20 前置）。
set -eu
export PATH="/opt/homebrew/bin:/usr/local/bin:$PATH"
cd /Users/sqlrush/airush
helm lint deploy/charts/airush -f deploy/charts/airush/values-dev.yaml
helm template airush deploy/charts/airush -f deploy/charts/airush/values-dev.yaml > /tmp/airush-rendered.yaml
echo "rendered lines: $(wc -l < /tmp/airush-rendered.yaml)"
echo "== llm ConfigMap =="
awk '/name: airush-llm-config/,/^---/' /tmp/airush-rendered.yaml | head -60
echo "== 渲染物里的 sk-/api_key 明文（应只有 os.environ 引用与 mock）=="
grep -n "api_key\|master_key" /tmp/airush-rendered.yaml
