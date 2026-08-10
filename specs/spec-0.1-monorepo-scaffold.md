# spec-0.1 monorepo 脚手架与构建体系

> **DRAFT — 待 user approve**

## Header / 元数据

- **位置**：Stage 0 第 1 个功能点，无前置 spec；产出的目录结构与构建约定被后续所有 spec 依赖；
- **配套规则**：CLAUDE.md 规则 1（5 阶段流程）、规则 2（本 spec 即首个按模板起草的 spec）、规则 8（多语言 lint 工具在 spec-0.2 定版，本 spec 只预留位置）；
- **关联文档**：`docs/2026-08-08-airush-platform-design.md`（§3 技术栈）、`docs/development-roadmap.md`（§2.1）；
- **决策日期**：2026-08-08，待 user approve；
- **修订**：2026-08-09 对齐 AD-11（agent 核心 = codexgo 抽核，Go）——`agent-runtime/`
  由 Python 改为 Go 模块，Python workspace 仅保留 `skills/`（记忆服务 Stage 3 引入时再建目录）。

## §1 范围

### §1.1 包含（Deliverables）

| # | Deliverable | 文件清单 | 估算 | 说明 |
|---|---|---|---|---|
| D1 | 顶层目录结构 | `console/` `connector/` `gateway/` `agent-runtime/` `skills/` `frontend/` `deploy/` `proto/` 各含占位 README | ~10 文件 | 按可部署组件划分，见 §8 Q1/Q2 |
| D2 | Go workspace | `go.work`、`console/go.mod`、`connector/go.mod`、`gateway/go.mod`、`agent-runtime/go.mod`（AD-11：codexgo 抽核宿主），各含一个可编译的 `cmd/<name>/main.go`（打印版本退出，~20 LOC/个） | ~9 文件 | Go 1.23+ |
| D3 | Python workspace | `skills/pyproject.toml`（uv workspace，根 `pyproject.toml` 聚合）、可 import 的最小包 + 一个冒烟测试 | ~5 文件 | Python 3.12+，uv 管理 |
| D4 | 前端脚手架 | `frontend/package.json`（pnpm + Vite + React + TS 最小工程，可 build） | ~10 文件 | 仅骨架，UI 从 spec-1.13 开始 |
| D5 | 根构建入口 | `Makefile`（targets 见 §2.2）、`.editorconfig`、`.gitignore` | 3 文件 ~120 行 | 唯一构建入口约定 |
| D6 | 仓库门面 | `README.md`（项目简介 + 快速开始 + 文档导航） | 1 文件 ~60 行 | — |

### §1.2 不包含

| 不做的事 | 理由 |
|---|---|
| lint 规则配置（golangci-lint/ruff/eslint） | spec-0.2 专项定版，避免本 spec 膨胀 |
| CI workflow | spec-0.3 专项；本地 make 先跑通 |
| Dockerfile / Helm | spec-0.10 专项；脚手架期无部署需求 |
| proto 文件内容与代码生成链 | 首个跨服务接口出现时（spec-1.2/1.9）再定，现在只留目录 |
| 共享 Go 库（`libs/`） | 出现第二个使用者前不抽公共库，避免过早抽象 |
| 前端组件库/路由/状态管理选型 | spec-1.13 按真实页面需求选，脚手架期选了也会推翻 |

### §1.3 例外说明

无偏离总体设计的项。技术栈版本（Go 1.23+/Python 3.12+/Node 22+）为本 spec 新定，
后续 spec 不得静默变更。

## §2 接口设计

### §2.1 目录结构（定版）

```
airush/
├── console/        # Go：控制面 API
├── connector/      # Go：客户侧采集/执行代理
├── gateway/        # Go：Connector 接入网关
├── agent-runtime/  # Go：智能体运行时（codexgo 抽核宿主，AD-11）
├── skills/         # Python：skill 服务集（每个 skill 一个子包）
├── frontend/       # React+TS：控制台前端
├── proto/          # 跨服务 gRPC/protobuf 契约（唯一来源）
├── deploy/         # Helm charts、kind 配置（spec-0.10 填充）
├── docs/  specs/   # 文档与 spec（已存在）
├── go.work  Makefile  .editorconfig  .gitignore  README.md
```

### §2.2 Makefile targets（定版，后续 spec 只增不改语义）

| target | 语义 |
|---|---|
| `make build` | 构建全部组件（Go 编译 + Python 检查 + 前端 build） |
| `make test` | 全部单元测试 |
| `make lint` | 全部 lint（spec-0.2 前为 no-op 占位，显式打印 SKIP） |
| `make fmt` | 全部格式化 |
| `make clean` | 清理构建产物 |
| `make <component>/build` 等 | 单组件粒度操作（如 `make console/test`） |

## §3 行为契约

- 全新 checkout 后 `make build && make test` 在仅安装了 Go/uv/pnpm 的机器上一次通过；
- 任何 target 失败以非零码退出且输出可定位的错误（禁静默失败）；
- 占位 target（如 lint）必须显式打印 `SKIP: defined in spec-0.2`，不得假装成功通过；
- `go.work` 覆盖全部 Go 模块——新增 Go 模块而不更新 go.work 视为 D2 契约破坏。

## §4 测试用例

| # | 用例 | 目的 |
|---|---|---|
| T1 | `make build` 全新环境通过 | 构建链完整性 |
| T2 | `make test` 通过且含 ≥1 Go / ≥1 Python 冒烟用例 | 测试骨架可用 |
| T3 | 四个 Go 二进制 `--version` 输出版本号 | cmd 入口约定成立 |
| T4 | `uv sync` 后 `skills` 包可 import | Python workspace 成立 |
| T5 | `pnpm build` 产出 `frontend/dist` | 前端链路成立 |

## §5 与现有代码的 contract

仓库现仅有 `docs/` `specs/` `CLAUDE.md`，本 spec 全部为新增，不修改既有文件
（`.gitignore` 为新建）。目录名一经 approve 即为后续所有 spec 的引用基准，改名需修订本 spec。

## §6 风险

| 风险 | 概率 | 缓解 |
|---|---|---|
| 目录结构不适配后期演进（如 skills 需独立仓库发布） | 中 | monorepo 内以组件为边界，迁出成本=移目录+改 CI；§8 Q1 已论证 |
| Go workspace 与 CI 缓存交互不佳导致构建慢 | 低 | spec-0.3 做 CI 时按模块分缓存 key |
| uv 尚在快速迭代，锁文件格式变更 | 低 | 锁定 uv 版本于 Makefile 检查中，升级走 PR |
| 三语言工具链版本漂移（本地 vs 未来 CI） | 中 | README 声明版本 + `make doctor` 检查工具存在与版本下限 |
| 占位 main.go/包被误当成真实现基础扩展 | 中 | 占位文件带注释标明"scaffold placeholder"，对应 spec 实装时整体替换 |

## §7 DoD

- [ ] D1-D6 全部文件就位，目录与 §2.1 完全一致
- [ ] T1-T5 全部通过（本地执行记录附在 PR）
- [ ] `make doctor` 能检出缺失工具并给出安装提示
- [ ] 占位 lint target 显式打印 SKIP
- [ ] README 含 5 分钟快速开始（clone → make build → make test）
- [ ] `.gitignore` 覆盖三语言构建产物 + IDE + secret 文件模式
- [ ] 无任何硬编码 secret / 真实配置值入库
- [ ] specs/README.md 状态表更新
- [ ] roadmap 第 8 节进度表更新
- [ ] commit 按 `<type>: <description>` 格式，本 spec 单独一个 PR/commit 序列

## §8 Q&A

**Q1 仓库形态：A. monorepo（★推荐） B. 按组件多仓库**
推荐 A：6 个组件共享 proto 契约与部署配置，多仓库会把每次跨服务改动变成多 PR 联动；
参考设计文档 AD 表全部决策都假设统一演进。团队规模（1-2 人）下多仓库纯增开销。

**Q2 目录组织：A. 按可部署组件（★推荐） B. 按语言（go/、python/） C. 按分层（services/、libs/）**
推荐 A：与 k8s 部署单元一一对应，spec/CI/Helm 均可按组件寻址；B 把部署单元切散在语言目录下，
C 在没有共享库的现阶段是过早结构。

**Q3 Python 包管理：A. uv（★推荐） B. poetry C. pip-tools**
推荐 A：workspace 支持天然匹配 agent-runtime+skills 多包结构，解析与安装速度显著优于 B/C，
锁文件可靠；风险（快速迭代）已列入 §6 并有缓解。

**Q4 构建入口：A. 根 Makefile（★推荐） B. Taskfile/just C. 各组件各自为政**
推荐 A：make 无处不在、CI 友好、心智成本最低；B 需额外安装工具与本项目"最小工具链"目标冲突；
C 会让 CI 与新人上手路径发散。

**Q5 前端构建：A. Vite（★推荐） B. Next.js**
推荐 A：控制台是纯内部 SPA，无 SSR/SEO 需求，Vite 更轻；若未来出现营销站/文档站需求另立组件。

## §9 实施计划

| 步骤 | 内容 | 估时 |
|---|---|---|
| 1 | D1+D5+D6（目录、Makefile、README、gitignore） | 0.5 天 |
| 2 | D2 Go workspace + 三个占位二进制 + 冒烟测试 | 0.5 天 |
| 3 | D3 Python uv workspace + 冒烟测试 | 0.5 天 |
| 4 | D4 前端脚手架 | 0.5 天 |
| 5 | T1-T5 验证 + `make doctor` + DoD 清单收尾 | 0.5 天 |

总计 2.5 天，与 roadmap Stage 0 ~6 周（12 specs）估算持平。

## §10 后续 spec 关联

- spec-0.2（lint）：接管本 spec 的 lint 占位 target；
- spec-0.3（CI）：以 `make build/test/lint` 为 CI 步骤唯一入口；
- spec-0.10（镜像/Helm）：按 §2.1 组件目录逐个建 Dockerfile；
- spec-1.2 / spec-1.9：填充 `proto/` 并建立代码生成链；
- 全部 Stage 1 spec：在对应组件目录内实装，替换占位入口。
