#!/bin/zsh
# Unit tests (race) for the Go modules on the Mac host. Args: module dirs.
set -eu
export PATH="/opt/homebrew/bin:/usr/local/bin:$PATH"
export GOFLAGS=-mod=readonly
cd /Users/sqlrush/airush

mods=${@:-"libs/apierror libs/obs libs/metrics libs/tenancy libs/llm testkit console gateway connector"}
for m in ${=mods}; do
  echo "==> test $m"
  (cd "$m" && go test -race ./...)
done
