#!/bin/zsh
# Run a query against the local openGauss 5.0.3 container (og5).
# Dev-only helper: the credential is the throwaway container's own builtin one
# (GS_PASSWORD in its env), never platform-managed config.
# Usage: og5-query.sh "SELECT 1" [database]
set -eu
export PATH="/usr/local/bin:/opt/homebrew/bin:$PATH"
DB=${2:-postgres}
docker exec og5 su - omm -c \
  "/usr/local/opengauss/bin/gsql -p 5432 -d $DB -c \"$1\""
