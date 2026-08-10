#!/bin/zsh -l
# 在 Mac 宿主机上执行 spec-0.1 全量验证（T1/T2）。
# 用绝对路径 cd，杜绝 ssh 非交互 shell 的 cwd 歧义。
set -euo pipefail
cd /Users/sqlrush/airush

echo "== clean cross-arch artifacts =="
rm -rf .venv frontend/node_modules frontend/dist bin

echo "== make doctor =="
make doctor

echo "== make build (T1) =="
make build

echo "== make test (T2) =="
make test

echo "== ALL PASS =="
