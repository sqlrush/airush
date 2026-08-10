#!/bin/zsh -l
# spec-0.12 D1：Stage 0 验收自动项（A1-A7，见 docs/stage-0-acceptance.md）。
# 幂等：dev 环境残留自动重建；任一 FAIL 指向对应 spec 并非零退出。
set -uo pipefail
export PATH="/opt/homebrew/bin:/usr/bin:/bin:/usr/sbin:/sbin:$HOME/.local/bin:$PATH"
cd /Users/sqlrush/airush

pass=0; failed=0
ok()   { echo "  PASS  $1"; pass=$((pass+1)); }
bad()  { echo "  FAIL  $1  → $2"; failed=$((failed+1)); }

echo "== 环境指纹 =="
echo "  $(sw_vers -productVersion 2>/dev/null || uname -sr) | docker $(docker version --format '{{.Server.Version}}' 2>/dev/null) | kind $(kind --version | awk '{print $3}') | helm $(helm version --short 2>/dev/null | head -c 12) | go $(/opt/homebrew/bin/go env GOVERSION)"

echo "== A1 三语言构建/测试/lint =="
if ./deploy/scripts/mac-run.sh build >/dev/null 2>&1 \
   && ./deploy/scripts/mac-run.sh cover >/dev/null 2>&1 \
   && ./deploy/scripts/mac-run.sh lint >/dev/null 2>&1; then
  ok "make build/cover/lint 全绿"
else
  bad "A1" "spec-0.1/0.2/0.4"
fi

echo "== A3 make dev-up 一键 kind 全栈 =="
./deploy/scripts/mac-run.sh dev-down >/dev/null 2>&1 || true
if ./deploy/scripts/mac-run.sh dev-up >/dev/null 2>&1; then
  ok "dev-up 从零到就绪"
else
  bad "A3" "spec-0.10"
fi

echo "== A3+/A5 集群健康 + 迁移落库（dev-verify） =="
if ./deploy/scripts/dev-verify.sh >/dev/null 2>&1; then
  ok "pods/migrate/healthz/幂等/securityContext"
else
  bad "A3+/A5" "spec-0.6/0.10"
fi

echo "== A4 三信号可见（obs-smoke） =="
./deploy/scripts/mac-run.sh obs-up >/dev/null 2>&1
sleep 15
if ./deploy/scripts/obs-smoke.sh >/dev/null 2>&1; then
  ok "Tempo/Prometheus/Loki 三信号 + 脱敏"
else
  bad "A4" "spec-0.9"
fi

echo "== A5b 集成测试（testcontainers 全量） =="
if ./deploy/scripts/mac-run.sh integration-test >/dev/null 2>&1; then
  ok "integration-test 全绿"
else
  bad "A5b" "spec-0.5/0.6"
fi

echo "== A6 发布链路产物 =="
if gh release view v0.0.1-rc.1 --json assets --jq '.assets[].name' 2>/dev/null | grep -q 'airush-0.0.1-rc.1.tgz'; then
  ok "rc release + chart 包就位"
else
  bad "A6" "spec-0.11"
fi

echo "== A7 spec 状态一致性（roadmap §8 进度表） =="
unimpl=$(sed -n '/^## 8/,$p' docs/development-roadmap.md \
  | grep -E '^\| spec-0\.[0-9]+' | grep -v -e 已实施 -e 验收 | wc -l | tr -d ' ')
if [ "$unimpl" -eq 0 ]; then
  ok "进度表全部 Stage 0 spec 已实施/验收中"
else
  bad "A7" "进度表存在 $unimpl 个未实施 spec"
fi

echo ""
echo "== 汇总: PASS=$pass FAIL=$failed =="
[ $failed -eq 0 ] && echo "STAGE 0 AUTO-ACCEPTANCE ALL PASS（手工项与签署见 docs/stage-0-acceptance.md）"
exit $failed
