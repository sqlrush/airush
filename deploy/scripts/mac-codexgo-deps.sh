#!/bin/zsh
# spec-1.8 §9 步骤 1：抽核目标包的第三方模块依赖清单（规则 5 硬门槛 #4 的输入）。
# 只读：go list 只读模块图，不改任何文件。输出两组：目标包依赖的第三方模块 / 其中 airush 现有依赖集之外的。
#
# 目标包 = airush agent-runtime 将 import 的 codexgo 包（D0.9 缝合后）。不在列表里的
# internal/state（SQLite）、secrets（age/keyring）、otel（airush 用自家 spec-0.9 观测）、
# agentidentity（chatgpt JWT）、core/localexec（本地命令执行，AD-9 不带走）不属于抽核范围。
set -eu
export PATH="/opt/homebrew/bin:/usr/local/bin:$PATH"
cd /Users/sqlrush/codexgo
PKGS="./internal/core ./internal/core/coretest ./internal/protocol/... ./internal/api ./internal/client/... ./internal/mcp/... \
 ./internal/multiagent/... ./internal/agentgraph ./internal/agentgraph/agentgraphtest ./internal/threadstore ./internal/rollout/... \
 ./internal/config/... ./internal/modelproviderinfo/... ./internal/modelsmanager/... ./internal/skills/... \
 ./internal/tools/... ./internal/msghistory/... ./internal/hooks/... ./internal/features/..."
# airush 已有（各 go.mod 直接或间接）+ 2026-08-16 user 批准的三项
APPROVED="github.com/google/uuid|github.com/klauspost/compress|github.com/pelletier/go-toml/v2|github.com/rivo/uniseg|golang.org/x/sys|golang.org/x/text"
echo "== 目标包引用的 codexgo 内部包（看有没有把'不要'的包拖进来）=="
go list -deps ${=PKGS} 2>&1 | grep "^github.com/sqlrush/codexgo/internal/" | sed 's#github.com/sqlrush/codexgo/internal/##' | sort -u | tr '\n' ' '; echo
echo "== 第三方模块（去 std/内部）=="
go list -deps -f "{{if not .Standard}}{{if .Module}}{{.Module.Path}}{{end}}{{end}}" ${=PKGS} 2>&1 | grep -v "^github.com/sqlrush/codexgo$" | sort -u > /tmp/codexgo-third.txt
wc -l < /tmp/codexgo-third.txt
cat /tmp/codexgo-third.txt
echo "== 未审（不在 airush 现有 + 已批集合内）=="
grep -vE "^($APPROVED)$" /tmp/codexgo-third.txt || echo "(无)"
