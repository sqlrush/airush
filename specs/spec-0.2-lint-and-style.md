# spec-0.2 代码风格与 lint

> **approved & shipped** — Stage 0 验收签署 2026-08-10（分级预批流程，spec-0.12）

## Header / 元数据

- **位置**：Stage 0 第 2 个功能点；前置 spec-0.1（目录结构与 `make lint` 占位）；
  产出的 lint 配置被 spec-0.3（CI）作为闸门直接调用；
- **配套规则**：CLAUDE.md 规则 8（多语言 lint 定版于本 spec）、`docs/development-standards.md`
  §2/3/4（各语言规范条款，本 spec 把条款翻译成工具配置）、§0（vendored/生成代码豁免）；
- **关联文档**：`docs/decoupling-architecture.md` §2.2（depguard 承载解耦边界）；
- **决策日期**：2026-08-09。

## §1 范围

### §1.1 包含（Deliverables）

| # | Deliverable | 文件清单 | 估算 | 说明 |
|---|---|---|---|---|
| D1 | Go lint 配置 | 根 `.golangci.yml`（覆盖 console/connector/gateway/agent-runtime 四模块） | 1 文件 ~90 行 | 必开：errcheck、govet、staticcheck、depguard、gosec、revive；见 §2.1 |
| D2 | Python lint/类型配置 | 根 `pyproject.toml` 增 `[tool.ruff]`（含 isort/pyupgrade 规则集）与 `[tool.mypy]`（strict） | 1 文件 ~60 行增量 | 全 Python 包统一继承，见 §8 Q2 |
| D3 | 前端 lint/格式配置 | `frontend/eslint.config.js`（flat config，typescript-eslint strict）、根 `.prettierrc` | 2 文件 ~80 行 | 禁 `any`；`noImplicitAny`/`strictNullChecks` 在 tsconfig 同步开启 |
| D4 | Makefile lint/fmt 实装 | `Makefile` 修改：`lint`/`fmt` 从 SKIP 占位改为真实执行，含 `make lint-go/lint-py/lint-fe` 分语言入口 | ~40 行增量 | 接管 spec-0.1 占位契约 |
| D5 | 豁免机制 | `.golangci.yml`/`pyproject.toml`/eslint 各自的 exclude 段：`agent-runtime/internal/codexcore/**`（vendored 预留）、`**/*_gen.go`、`**/*.pb.go`、`frontend/src/api/generated/**` | 配置内嵌 | development-standards §0 的机器可执行化 |

### §1.2 不包含

| 不做的事 | 理由 |
|---|---|
| CI 中执行 lint | spec-0.3 专项；本 spec 只保证 `make lint` 本地语义正确 |
| depguard 完整规则集 | 解耦边界规则依赖 Stage 1 真实包路径；本 spec 只立框架 + 首条示例规则（禁止 console 直接 import agent-runtime internal），后续 spec 增量补 |
| pre-commit hooks 框架 | §8 Q4 决策不引入：与"最小工具链"目标冲突，闸门由 make + CI 承担 |
| commit message lint | 规则 8 已约定格式，靠 review 执行；工具化收益低于维护成本 |
| 镜像/依赖安全扫描（trivy/audit 类） | 属 CI/镜像链路，spec-0.3 与 spec-0.10 分别承载 |
| 编辑器强制配置（VS Code settings 入库） | `.editorconfig`（spec-0.1）已覆盖基线；编辑器偏好不入库 |

### §1.3 例外说明

无偏离。vendored codexgo 路径 `agent-runtime/internal/codexcore/` 在 Stage 1（spec-1.8）
才出现，本 spec 预写排除规则（排除不存在的路径无副作用），避免届时改 lint 配置引发全量重跑。

## §2 接口设计

### §2.1 golangci-lint 启用清单（定版）

| linter | 用途 | 备注 |
|---|---|---|
| errcheck / govet / staticcheck | 错误处理与静态缺陷基线 | development-standards §2 强制 |
| depguard | 解耦边界（decoupling §2.2） | 规则增量维护，违规 = 架构违规 |
| gosec | 安全（硬编码凭据、弱随机等） | 告警不可 nolint 静默，需注释理由 |
| revive | 命名/注释/尺寸类风格 | 函数 50 行/文件 800 行阈值在此配置 |
| gci + gofumpt | import 分组与格式 | `make fmt` 统一执行，禁手工格式争议 |

### §2.2 关键配置决策

- mypy：`strict = true` 全局；第三方无 stub 的库（FastMCP 等）用 `[[tool.mypy.overrides]]`
  按模块显式 `ignore_missing_imports`，禁全局关闭；
- ruff 规则集：`E,F,W,I,UP,B,S`（pycodestyle/pyflakes/isort/pyupgrade/bugbear/bandit 子集）；
- eslint：`typescript-eslint` strict preset + `no-explicit-any: error`（豁免须行内 disable + 理由）；
- 所有 lint 违规 = 构建失败，无 warning 级别（warning 会被永久忽视）。

## §3 行为契约

- `make lint` 零违规退出 0，任一违规退出非 0 并输出文件:行号；
- `make fmt` 幂等：连续执行两次，第二次零 diff；
- 豁免路径内的文件不产生任何 lint 输出（含未来 vendored 代码）；
- lint 工具版本固定（见 §8 Q1），`make doctor` 校验版本不符即报错——本地与 CI 结论一致性是硬契约。

## §4 测试用例

| # | 用例 | 目的 |
|---|---|---|
| T1 | 现有占位代码 `make lint` 全绿 | 基线可用 |
| T2 | 注入含未处理 error 的 Go 文件 → lint 失败并指出行号 | errcheck 闸门有效 |
| T3 | 注入含 `any` 的 TS 文件 → lint 失败 | 前端严格性有效 |
| T4 | 注入无类型注解的 Python 函数 → mypy 失败 | strict 有效 |
| T5 | 在豁免路径放置违规文件 → lint 通过 | 豁免机制有效 |
| T6 | `make fmt` 两次执行，第二次 `git diff` 为空 | 幂等性 |
| T7 | console 中 import agent-runtime internal 包 → depguard 报错 | 解耦框架成立 |

## §5 与现有代码的 contract

- 修改：`Makefile`（lint/fmt 从占位转实装——spec-0.1 §2.2 预留的演进）、根 `pyproject.toml`；
- 新增：`.golangci.yml`、`frontend/eslint.config.js`、`.prettierrc`；
- 不动：目录结构、go.work、任何占位业务代码（lint 修复除外）；
- 兼容性：`make lint` 的调用签名与退出码语义即 spec-0.3 CI 的依赖接口，定版后不变。

## §6 风险

| 风险 | 概率 | 缓解 |
|---|---|---|
| golangci-lint 大版本升级破坏配置格式（v1→v2 已发生过） | 中 | 版本固定 + 升级走独立 PR 附全量 lint 对比 |
| mypy strict 对 skill 生态库（FastMCP/graphiti SDK）类型缺失导致大量豁免 | 中 | 按模块 overrides 白名单管理，豁免数入 review checklist，超 10 条触发重估 |
| revive 尺寸阈值对未来 vendored 代码误报 | 高 | D5 豁免路径预写，Stage 1 vendor 时零配置变更 |
| eslint flat config 与部分插件兼容问题 | 低 | 插件面窄（ts + react-hooks + prettier），锁 minor 版本 |
| 三工具格式化互相打架（prettier vs editorconfig vs gofumpt） | 低 | 分域：Go=gofumpt、Py=ruff format、TS=prettier，editorconfig 只管缩进/换行基线 |

## §7 DoD

- [ ] D1-D5 配置文件就位且 `make lint` 全绿
- [ ] T1-T7 全部通过（注入类用例执行后移除，记录附 PR）
- [ ] lint 工具版本固定并纳入 `make doctor`
- [ ] 分语言入口 `make lint-go/lint-py/lint-fe` 可独立执行
- [ ] 豁免路径清单与 development-standards §0 一致
- [ ] depguard 首条规则生效（T7）
- [ ] `make fmt` 幂等（T6）
- [ ] 无 warning 级配置残留（全部 error 级）
- [ ] specs/README.md 与 roadmap 进度表更新
- [ ] commit 格式合规，独立 commit 序列

## §8 Q&A

**Q1 lint 工具版本管理：A. 固定版本 + make doctor 校验（★推荐） B. 跟随 latest**
推荐 A：lint 结论的本地/CI 一致性是契约（§3），latest 会造成"本地绿 CI 红"；
升级收益低频，走显式 PR 成本可控。

**Q2 Python lint 配置位置：A. 根 pyproject 统一（★推荐） B. 各包独立配置**
推荐 A：skills 多子包共享一套规则避免漂移；将来某包确需差异用 per-package overrides
显式声明，而非整包独立配置。

**Q3 eslint 配置形态：A. flat config（★推荐） B. legacy .eslintrc**
推荐 A：eslint 9 默认形态，legacy 已进入弃用通道；新项目无历史包袱。

**Q4 pre-commit hooks：A. 不引入（★推荐） B. pre-commit 框架**
推荐 A：闸门由 `make lint` + CI 承担已足够；pre-commit 增加新人环境配置步骤，
且 hook 可被 `--no-verify` 绕过，不构成真实闸门。

**Q5 mypy 严格度：A. strict 全开 + 显式模块豁免（★推荐） B. basic 起步渐进收紧**
推荐 A：渐进收紧在实践中永远"来不及收"；strict 从第一行代码执行的成本最低，
豁免白名单让例外可见可审计。

## §9 实施计划

| 步骤 | 内容 | 估时 |
|---|---|---|
| 1 | D1 golangci 配置 + T1/T2/T7 | 0.5 天 |
| 2 | D2 ruff/mypy + T4 | 0.25 天 |
| 3 | D3 eslint/prettier + T3 | 0.25 天 |
| 4 | D4/D5 Makefile 实装 + 豁免 + T5/T6 + DoD 收尾 | 0.5 天 |

总计 1.5 天。

## §10 后续 spec 关联

- spec-0.3（CI）：`make lint` 为 CI lint 步骤唯一入口，退出码即闸门；
- spec-1.8（agent-runtime）：vendored 路径豁免即时生效，无需配置变更；
- spec-1.2/1.9（proto 生成链）：生成代码排除模式已预留；
- decoupling-architecture：每个解耦点落地时向 depguard 增补规则（增量、有据可查）。
