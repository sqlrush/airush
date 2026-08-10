#!/usr/bin/env bash
# spec-0.3 D3：main 分支保护即代码（幂等，重放无 diff）。
# 语义：必须 PR 且 ci/lint、ci/test、ci/build 全绿才能进 main；含管理员；禁 force push。
set -euo pipefail
REPO="sqlrush/airush"

gh api -X PUT "repos/$REPO/branches/main/protection" \
  -H "Accept: application/vnd.github+json" \
  --input - <<'JSON'
{
  "required_status_checks": {
    "strict": true,
    "contexts": ["ci/lint", "ci/test", "ci/build", "security/gitleaks"]
  },
  "enforce_admins": true,
  "required_pull_request_reviews": null,
  "restrictions": null,
  "allow_force_pushes": false,
  "allow_deletions": false,
  "required_linear_history": false,
  "required_conversation_resolution": false
}
JSON

echo "branch protection applied to $REPO main"
gh api "repos/$REPO/branches/main/protection" --jq \
  '{checks: .required_status_checks.contexts, enforce_admins: .enforce_admins.enabled, force_push: .allow_force_pushes.enabled}'
