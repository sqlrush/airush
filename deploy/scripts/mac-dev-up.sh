#!/bin/zsh
# Bring up the kind dev stack and run dev-verify on the Mac host.
# docker/kubectl live in /usr/local/bin; kind/helm in /opt/homebrew/bin.
set -eu
export PATH="/opt/homebrew/bin:/usr/local/bin:$PATH"
export GOFLAGS=-mod=readonly
cd /Users/sqlrush/airush

make dev-up
bash deploy/scripts/dev-verify.sh
