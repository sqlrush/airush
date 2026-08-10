# Stage 0 验收（spec-0.12）

> 自动项由 `deploy/scripts/stage0-acceptance.sh` 执行（Mac 宿主机）；
> 手工项逐条实测记录；**验收通过 = 自动项全 PASS + 手工项全勾 + user 签署**。

## 1. 自动项（A1-A7）

| # | 项 | 验证方式 | 结果 |
|---|---|---|---|
| A1 | 三语言可构建/测试/lint | `make build && make cover && make lint` | **PASS**（2026-08-10） |
| A3 | `make dev-up` 一键 kind 全栈 | 从 dev-down 干净态重建 | **PASS**（2026-08-10） |
| A3+/A5 | 集群健康 + PG 迁移落库 | dev-verify.sh（pods/migrate v1/healthz/幂等/securityContext） | **PASS**（2026-08-10） |
| A4 | 日志/指标/trace 三信号可见 | obs-smoke.sh（Tempo/Prom/Loki 同 trace + 脱敏断言） | **PASS**（2026-08-10） |
| A5b | 集成测试全量 | `make integration-test`（testcontainers PG/Redis） | **PASS**（2026-08-10） |
| A6 | 发布链路可用 | v0.0.1-rc.1 pre-release + chart 包存在 | **PASS**（2026-08-10） |
| A7 | spec 状态一致 | roadmap 进度表无未实施遗留（除本 spec） | **PASS**（2026-08-10） |

## 2. 手工项（执行记录）

- [x] **A2 分支保护生效**：直推 main 被拒（`protected branch hook declined, 4 of 4
  required status checks are expected`，2026-08-10 实测）；红灯 PR 不可合并（PR #1
  双红验证）；含管理员；禁 force push
- [x] **CI 全链路**：PR #1-#10 全部经四 required checks + gitleaks 合并；main
  image job 6/6 推送 ghcr
- [x] **三信号 Grafana 可视**：obs-smoke 通过即三数据源可查（Grafana :3000 人工
  复核入口已在 README）

## 3. 基线数据（D4，Stage 1 回归对比基准）

| 指标 | 数值 |
|---|---|
| CI 冷跑（首次，无缓存） | lint 149s / test 29s / build 34s |
| CI 暖跑（近期典型） | lint ~100-125s / test ~85s / build ~30s / integration ~90s |
| 镜像体积 | console 19.4MB / gateway 27.4MB / frontend 77.6MB / skills 222MB |
| dev-up 端到端（含镜像构建，热缓存） | ~1-2 分钟（首次含 kind node 拉取另计） |
| 单测+覆盖率全量（Mac） | <1 分钟 |
| 覆盖率基线 | libs/config 87.4% · libs/apierror 94.6% · libs/obs 82.7%（阻断 spec-1.1 起激活） |

## 4. 欠账清单（§2.2 分级）

| 级 | 事项 | 去向 |
|---|---|---|
| P2 | gateway/console 模块单测覆盖率低（服务逻辑由集成/冒烟覆盖，报告模式如实显示 BELOW） | spec-1.1/1.2 实装业务逻辑时自然补齐 |
| P2 | 上游 actions 的 Node20 弃用告警（checkout@v4 等） | 上游发新版跟随升级，无功能影响 |
| P2 | Python OTel trace/metric 导出未接（skills 无服务进程） | spec-1.9 首个 MCP server 时接入（spec-0.9 修订已记） |
| P2 | otel-lgtm 镜像未钉版 | 出现拉取/兼容问题时钉（spec-0.10 注释已记） |

P0/P1：无。

## 5. 签署

- [ ] **user 验收确认**（勾选并回复"验收通过"即视为签署；通过后执行：
  release-prep 0.1.0 → CHANGELOG 落段 PR → 打 v0.1.0 正式 tag → 全 spec 置 shipped）
