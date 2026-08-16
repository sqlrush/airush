#!/bin/zsh
# libs/llm 集成测试：真 LiteLLM 容器 + 进程内 mock。Args: -run 正则。
set -eu
export PATH="/opt/homebrew/bin:/usr/local/bin:$PATH"
export GOFLAGS=-mod=readonly
export TESTCONTAINERS_RYUK_DISABLED=${TESTCONTAINERS_RYUK_DISABLED:-true}
cd /Users/sqlrush/airush/libs/llm
go vet -tags=integration ./... && go test -tags=integration -count=1 -v -run "${1:-.}" ./... 2>&1 \
  | grep -vE "^20[0-9]{2}/|🐳|✅|⏳|🔔|🚫|Shell not found|Server Version|API Version|Operating System|Total Memory|Testcontainers for Go|Resolved Docker|Test SessionID|Test ProcessID" | tail -60
