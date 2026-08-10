# spec-0.3 CI/CD pipeline

> **DRAFT — 待 user approve**（Stage 0 分级预批：push 即可开工，user 异议后修改）

## Header / 元数据

- **位置**：Stage 0 第 3 个功能点；前置 spec-0.1（make 入口）、spec-0.2（lint 闸门）；
  为其后所有 spec 提供"CI 全绿才能合并"的执行机制（CLAUDE.md 规则 4）；
- **关联文档**：`docs/development-standards.md` §6（Git 与 PR）、CLAUDE.md 规则 8
  （依赖分层管控——传递依赖靠 CI 安全扫描兜底，本 spec 落地该扫描）；
- **决策日期**：2026-08-09。

## §1 范围

### §1.1 包含（Deliverables）

| # | Deliverable | 文件清单 | 估算 | 说明 |
|---|---|---|---|---|
| D1 | 主 CI workflow | `.github/workflows/ci.yml`：PR 与 push-main 触发，lint / test / build 三个 named job，按组件 path filter 增量执行（main 上全量） | 1 文件 ~150 行 | 全部步骤经 `make` 入口调用，CI 不写构建逻辑 |
| D2 | 安全扫描 workflow | `.github/workflows/security.yml`：gitleaks（secret 扫描）、govulncheck、pip-audit、pnpm audit；PR 阻断 + 每日 schedule | 1 文件 ~70 行 | 规则 8 传递依赖管控的机器执行面 |
| D3 | 分支保护即代码 | `deploy/scripts/setup-branch-protection.sh`（gh api 幂等脚本：main 必须 PR + required checks + include admins + 禁 force push） | 1 文件 ~40 行 | 配置可重放、可审计，不靠网页手点 |
| D4 | 缓存与并发策略 | ci.yml 内嵌：Go build/module cache（按 go.sum 分模块 key）、uv cache、pnpm store cache；`concurrency` 取消同 PR 旧运行 | 内嵌 | 私有仓免费额度保护，见 §6 |
| D5 | 镜像构建 job 骨架 | ci.yml 中 `image` job：矩阵按组件，**condition 为 Dockerfile 存在**（spec-0.10 落地后自动激活），推送 ghcr.io | ~40 行 | 骨架先行，避免 0.10 时改动 pipeline 结构 |
| D6 | 状态可见性 | README CI badge + PR 模板（`.github/pull_request_template.md`：spec 链接 + DoD 勾选 + 测试证据 + 新增前端依赖备案栏） | 2 文件 ~40 行 | development-standards §6 PR 要求的模板化 |

### §1.2 不包含

| 不做的事 | 理由 |
|---|---|
| CD 自动部署 | 当前无常驻部署环境；kind 验收在本地（spec-0.12），线上部署节奏 Stage 1 末再立 spec |
| release 发版流程（tag→版本产物） | spec-0.11 专项 |
| 覆盖率上报与阈值闸门 | 覆盖率口径与工具由 spec-0.4 定，其闸门作为 test job 的一部分在 0.4 接入 |
| self-hosted runner | 当前规模 github-hosted 足够；自建 runner 的维护成本与安全面不划算 |
| 集成测试 job | spec-0.5 定测试环境形态后，以独立 job 接入（本 spec 预留 job 名不实现） |
| 多架构镜像（arm64） | 生产目标 amd64；arm 需求出现（如 mac 本地调试镜像）再增，避免构建时间翻倍 |

### §1.3 例外说明

D5 镜像 job 依赖 spec-0.10 的 Dockerfile——属"骨架先行、跨 spec 激活"的刻意安排，
在两个 spec 中互相登记（0.10 §10 回链），不构成隐式依赖。

## §2 接口设计

### §2.1 job 结构（定版）

```
PR:   lint ──┐
      test ──┼──▶ 合并门槛（三个 required checks）
      build ─┘
main: lint+test+build（全量）──▶ image（按组件矩阵，Dockerfile 存在才跑）──▶ ghcr.io
每日: security（gitleaks + govulncheck + pip-audit + pnpm audit）
```

### §2.2 path filter 分组

| 组 | 触发路径 | 跑什么 |
|---|---|---|
| go | `console/** connector/** gateway/** agent-runtime/** go.work` | lint-go + go test + go build |
| py | `skills/** pyproject.toml uv.lock` | lint-py + pytest |
| fe | `frontend/**` | lint-fe + vitest + build |
| meta | `Makefile .golangci.yml .github/**` 等工具链文件 | 全量 |
| docs | `docs/** specs/** *.md` | 仅 markdown link check（不占额度） |

### §2.3 关键约定

- CI 步骤只允许调用 `make <target>`，禁止在 yml 里内联构建命令（保证本地=CI）；
- required checks 名称 `ci/lint`、`ci/test`、`ci/build` 定版——改名=破坏分支保护，需修订本 spec；
- secrets 仅 `GITHUB_TOKEN`（ghcr 推送用），无其他 secret；出现新 secret 需求走规则 5 硬门槛。

## §3 行为契约

- PR 任一 required check 红 → 不可合并（含管理员，include admins）；
- 直接 push main 被拒（分支保护），force push main 被拒；
- 同一 PR 新 push 自动取消进行中的旧运行（省额度）；
- docs-only 变更不触发编译类 job（额度保护），但 markdown 检查仍跑；
- gitleaks 命中 → PR 阻断且告警文本不回显 secret 内容本身。

## §4 测试用例

| # | 用例 | 目的 |
|---|---|---|
| T1 | 提交合规 PR → 三 check 绿 → 可合并 | 主链路 |
| T2 | 注入 lint 违规 PR → `ci/lint` 红 → 合并按钮禁用 | 闸门有效 |
| T3 | 直接 `git push origin main` → 被拒 | 分支保护生效 |
| T4 | 仅改 docs 的 PR → 编译类 job 跳过 | path filter 正确 |
| T5 | 注入伪造 secret（测试用假 token 格式）→ gitleaks 阻断 | secret 扫描有效 |
| T6 | 二次运行缓存命中（对比冷/热运行时长，记录数据） | 缓存策略有效 |
| T7 | 重跑 setup-branch-protection.sh → 幂等无 diff | 配置即代码 |

## §5 与现有代码的 contract

- 新增：`.github/` 全部内容、`deploy/scripts/setup-branch-protection.sh`；
- 依赖不修改：`make lint/test/build` 的语义（spec-0.1/0.2 契约）；
- 对后续 spec 的接口：required check 名称、job 骨架（integration/image 预留位）。

## §6 风险

| 风险 | 概率 | 缓解 |
|---|---|---|
| 私有仓 Actions 免费额度（2000 分钟/月）耗尽 | 中 | path filter + 缓存 + 并发取消 + docs 跳过；月中额度过半时告警（schedule job 检查用量 API），超限决策升级付费 |
| path filter 漏掉跨组件影响（改 go.work 只触发 go 组） | 中 | meta 组兜底全量；main 上永远全量跑，漏检在合并后暴露 |
| gitleaks 误报（测试 fixture 中的假密钥） | 中 | `.gitleaksignore` 按指纹白名单，白名单条目须注释理由并过 review |
| 缓存污染导致构建结果不一致 | 低 | cache key 含 lockfile hash；怀疑污染时手动 bump key 前缀 |
| ghcr 镜像拉取在国内网络慢（影响后续部署体验） | 中 | Stage 0 仅推送不依赖拉取；国内镜像同步（ACR）列入 spec-4.5 私有化议题 |
| required check 改名导致合并规则失效 | 低 | §2.3 定版名称，改名需修订本 spec 并重放 D3 脚本 |

## §7 DoD

- [ ] D1-D6 文件就位，T1-T7 全部通过（截图/运行链接附 PR）
- [ ] 三个 required checks 在分支保护中生效（含 admins）
- [ ] 冷/热缓存运行时长数据记录在 PR（作为 Stage 0 基线）
- [ ] security workflow 手动触发一次全绿（或白名单处理完误报）
- [ ] PR 模板含 spec 链接/DoD/测试证据/前端依赖备案四要素
- [ ] README 挂 CI badge
- [ ] CI 中无任何内联构建命令（纯 make 调用，review 核对）
- [ ] 无新增 secret（仅 GITHUB_TOKEN）
- [ ] specs/README.md 与 roadmap 进度表更新
- [ ] commit 格式合规，独立 commit 序列

## §8 Q&A

**Q1 CI 平台：A. GitHub Actions（★推荐） B. 自建（Drone/Woodpecker 等）**
推荐 A：仓库原生、分支保护/PR 深度集成、免维护；自建对 1-2 人团队是纯负担。
额度风险已列 §6 并有缓解与升级路径。

**Q2 PR 触发策略：A. path filter 增量 + main 全量（★推荐） B. 每次全量**
推荐 A：三语言全量一次 ~10+ 分钟且烧额度，增量把常规 PR 压到 3-5 分钟；
漏检风险由 main 全量兜底（发现即修，不过夜）。

**Q3 镜像 registry：A. ghcr.io（★推荐） B. Docker Hub C. 阿里云 ACR**
推荐 A：与 GITHUB_TOKEN 天然集成、私有镜像免费；B 私有额度限制多；
C 是私有化交付时的分发通道（Stage 4），不是开发期主 registry。

**Q4 安全扫描时机：A. PR 阻断 + 每日 schedule（★推荐） B. 仅每日**
推荐 A：secret 入库是 P0 事故（CLAUDE.md 安全原则 5），必须在合并前拦截；
依赖 CVE 类每日扫描即可（新 CVE 与代码变更无关）。

**Q5 required checks 粒度：A. lint/test/build 三个独立 check（★推荐） B. 单一聚合 check**
推荐 A：红灯直接指向阶段，重跑可单独重跑失败 job；B 省配置但排障要点进日志翻。

## §9 实施计划

| 步骤 | 内容 | 估时 |
|---|---|---|
| 1 | D1 主 workflow + D4 缓存/并发 + T1/T2/T4/T6 | 0.5 天 |
| 2 | D3 分支保护脚本 + T3/T7 | 0.25 天 |
| 3 | D2 安全扫描 + T5 + 误报白名单机制 | 0.25 天 |
| 4 | D5 镜像骨架 + D6 模板徽章 + DoD 收尾 | 0.5 天 |

总计 1.5 天。

## §10 后续 spec 关联

- spec-0.4：覆盖率闸门挂入 test job；
- spec-0.5：integration job 以预留名接入（触发时机在 0.5 §8 决策）；
- spec-0.10：Dockerfile 就位后 image job 自动激活，ghcr 命名规范在 0.10 定；
- spec-0.11：release workflow 复用本 spec 的构建缓存与镜像 job；
- 全部后续 spec：合并门槛即三 required checks，无豁免路径。
