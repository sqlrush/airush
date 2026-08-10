#!/bin/zsh -l
# Mac 宿主机统一执行入口：deploy/scripts/mac-run.sh <make 目标...>
# 绝对路径 cd（教训固化：禁止依赖 ssh 非交互 shell 的 cwd）。
set -euo pipefail
# 共享 home 防线：~/goroot 等 Linux 工具链目录可能混入 PATH，
# 显式把 Mac 原生工具目录置前（brew / system / ~/.local/bin 的 Mach-O 工具）。
export PATH="/opt/homebrew/bin:/usr/bin:/bin:/usr/sbin:/sbin:$HOME/.local/bin:$PATH"
# Go 入口硬钉（共享 home 陷阱总结）：
# - ~/goroot 是给 Linux VM 用的工具链，共享 shell rc 会在【每条 make recipe 的
#   shell 启动时】重新注入 GOROOT=~/goroot——wrapper 里 unset/export 全部无效；
# - 唯一免疫机制层的做法：行内 env 前缀，在命令执行时刻覆盖 GOROOT/GOTOOLCHAIN。
export GO="env GOROOT=/opt/homebrew/opt/go/libexec GOTOOLCHAIN=local /opt/homebrew/bin/go"
export TOOL_ENV="env PATH=/opt/homebrew/opt/go/libexec/bin:/opt/homebrew/bin:/usr/bin:/bin:/usr/sbin:/sbin GOROOT=/opt/homebrew/opt/go/libexec GOTOOLCHAIN=local"
cd /Users/sqlrush/airush
exec make "$@"
