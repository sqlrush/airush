# spec-0.4 单元测试框架与覆盖率门槛

> **DRAFT — 待 user approve**（Stage 0 分级预批：push 即可开工，user 异议后修改）

## Header / 元数据

- **位置**：Stage 0 第 4 个功能点；前置 spec-0.1（测试骨架占位）、spec-0.3（test job）；
  覆盖率闸门是 CLAUDE.md 规则 4（分层覆盖率）的机器执行面；
- **关联文档**：`docs/development-standards.md` §2（Go 表驱动 + t.Parallel）、§3（pytest）、
  §4（前端）、§0（生成代码/vendored 不计覆盖率）；
- **决策日期**：2026-08-09。

## §1 范围

### §1.1 包含（Deliverables）

| # | Deliverable | 文件清单 | 估算 | 说明 |
|---|---|---|---|---|
| D1 | Go 测试链 | `Makefile` 增 `test-go`/`cover-go`；`deploy/scripts/coverage-gate.sh`（读 coverprofile 按模块校验阈值，豁免路径剔除） | 2 文件 ~80 行 | `-race` 默认开启（§8 Q4） |
| D2 | Python 测试链 | 根 `pyproject.toml` 增 `[tool.pytest.ini_options]`（asyncio_mode=auto）与 `[tool.coverage]`（fail_under=80、omit 生成代码） | ~40 行增量 | pytest + pytest-asyncio + pytest-cov |
| D3 | 前端测试链 | `frontend/vitest.config.ts`：coverage 仅统计逻辑层（`src/**/*.ts`，排除 `*.tsx`/生成代码），thresholds 70 | 1 文件 ~50 行 | 规则 4 前端口径的落地（§8 Q5） |
| D4 | 统一入口与 CI 接入 | `make test`（三语言聚合）、`make cover`（含闸门）；spec-0.3 test job 改为调用 `make cover` | ~30 行增量 | 阈值失败=CI 红 |
| D5 | 规范样例测试 | 每语言 1 个示范：Go 表驱动+Parallel、pytest 参数化+async、vitest hook 测试；作为活文档被后续 spec 模仿 | 3 文件 ~120 行 | 放各组件占位包内 |

### §1.2 不包含

| 不做的事 | 理由 |
|---|---|
| 集成测试框架 | spec-0.5 专项（容器化依赖、build tag、执行时机都不同） |
| E2E/UI 测试 | UI 组件不计入强制指标（规则 4）；E2E 待 Stage 1 前端成型后单独立 spec |
| mutation testing | 对 1-2 人团队维护成本远超收益；断言质量靠 review checklist 抽查 |
| coverage 上报 SaaS（codecov 等） | §8 Q3 决策不引入：外部服务依赖 + 私有仓数据外发，本地阈值脚本等价 |
| benchmark/性能基线 | 规则 4 性能基线绑定"性能敏感路径"各自的 spec（采集/网关/LLM 网关），非通用框架 |
| flaky 测试隔离机制（quarantine 标签） | 出现第一个真实 flaky 用例时再立规则，避免为不存在的问题设计 |

### §1.3 例外说明

**口径修订（2026-08-10，spec-0.5/0.8 实施连带）**：§2.1 剔除清单增补——
`testkit` 模块整体豁免（测试基建，逻辑由集成态验证，单测覆盖率无意义）；
`*/gen/main.go`（代码生成器工具，装配层同类）。

**闸门激活时机偏离字面**（§8 Q2）：Stage 0 期间代码几乎全是脚手架占位，80% 阈值
无统计意义——覆盖率**报告**从本 spec 起生成，**阻断**从 Stage 1 首个业务 spec
（spec-1.1）合并起激活。此例外经本 spec 评审即视为 user 知情。

## §2 接口设计

### §2.1 覆盖率口径（定版）

| 层 | 范围 | 阈值 | 剔除 |
|---|---|---|---|
| Go 后端 | console/connector/gateway/agent-runtime 各模块独立计算 | ≥80%/模块 | `*_gen.go`、`*.pb.go`、`internal/codexcore/**`（vendored）、`cmd/*/main.go`（装配层） |
| Python 后端 | skills 各子包独立计算 | ≥80%/包 | 生成代码 |
| 前端逻辑层 | `src/**/*.ts`（hooks/状态/数据处理/工具） | ≥70% | `*.tsx` 组件、`src/api/generated/**`、样式与静态资源 |

**配套约定**：前端可测逻辑必须写在 `.ts` 文件（hooks/lib），`.tsx` 只做渲染拼装——
口径同时是架构约束，防"逻辑藏进组件逃避统计"。

### §2.2 测试组织约定

- Go：`_test.go` 邻近源码；表驱动 + `t.Parallel()` 默认（无法并行须注释理由）；
- Python：包内 `tests/` 目录；fixture 禁隐式全局状态（development-standards §3）；
- TS：`*.test.ts` 与被测文件同目录；
- 测试命名表达行为（`TestFoo_EmptyInput_ReturnsError`），禁 `Test1/TestOK`。

## §3 行为契约

- `make test` 任一语言失败即整体非零退出，输出定位到用例；
- `make cover` = test + 阈值校验；阈值不足输出"模块名 实际% 要求%"清单；
- `-race` 检出数据竞争视为测试失败（不可关闭合并）；
- 豁免路径变更（新增剔除项）必须修订本 spec §2.1 表格——剔除清单是受控口径，不是配置自由项。

## §4 测试用例

| # | 用例 | 目的 |
|---|---|---|
| T1 | 三语言样例测试经 `make test` 全通过 | 骨架可用 |
| T2 | 注入低覆盖模块 → `make cover` 报出模块名与差值 | 闸门计算正确 |
| T3 | 豁免路径内文件不影响覆盖率分母 | 剔除口径生效 |
| T4 | 注入数据竞争用例 → `-race` 失败 | race 检测有效 |
| T5 | async Python 用例免装饰器直接运行（asyncio_mode=auto） | 异步链路可用 |
| T6 | `.tsx` 文件不进前端覆盖率分母 | 前端口径生效 |
| T7 | CI test job 展示覆盖率摘要（PR 可见） | 可见性 |

## §5 与现有代码的 contract

- 修改：`Makefile`（test 占位转实装）、根 `pyproject.toml`、spec-0.3 的 test job 调用目标
  （`make test`→`make cover`，job 名不变，不破坏 required check）；
- 新增：coverage-gate.sh、vitest.config.ts、三个样例测试；
- 不动：lint 配置、目录结构；
- 对后续 spec 的接口：§2.1 口径表 + §2.2 组织约定为所有业务 spec 的测试规范基线。

## §6 风险

| 风险 | 概率 | 缓解 |
|---|---|---|
| 为凑数写无断言/弱断言测试，覆盖率失真 | 高 | review checklist 增"抽查 3 个用例断言密度"项；发现凑数用例按规则 4 测试红线处理 |
| 占位期阈值形同虚设造成"闸门失灵"错觉 | 中 | §1.3 例外显式声明激活时机，roadmap 进度表登记 |
| `-race` 拖慢测试（约 2-10 倍） | 低 | 单测规模小；变慢明显时拆 `make test-fast`（无 race）供本地快速迭代，CI 恒开 race |
| 前端逻辑写进 .tsx 绕过统计 | 中 | §2.1 架构约定 + review 核对；eslint 规则（组件文件禁复杂逻辑）列入 backlog 观察 |
| 各语言覆盖率工具口径差异（分支 vs 行） | 低 | 统一按行覆盖率；分支覆盖率作为参考输出不做闸门 |

## §7 DoD

- [ ] D1-D5 就位，T1-T7 通过（记录附 PR）
- [ ] `make cover` 在 CI test job 生效（报告模式）
- [ ] 阈值阻断开关就位（单变量激活，spec-1.1 时打开）
- [ ] 覆盖率剔除清单与 §2.1 完全一致
- [ ] 三语言样例测试符合 §2.2 全部约定（作为范本 review）
- [ ] `-race` 在 CI 默认开启
- [ ] 覆盖率摘要在 PR 可见（T7 截图）
- [ ] development-standards §2/3/4 与本 spec 无矛盾（复核一遍）
- [ ] specs/README.md 与 roadmap 进度表更新
- [ ] commit 格式合规，独立 commit 序列

## §8 Q&A

**Q1 阈值粒度：A. 按模块/包独立（★推荐） B. 仓库全局加权**
推荐 A：全局口径下大模块（如未来 console）会稀释小模块的缺口，闸门失去定位能力；
按模块红灯直接指向责任范围。

**Q2 闸门激活时机：A. 报告即刻、阻断从 spec-1.1 起（★推荐） B. 立即阻断**
推荐 A：对占位 main.go 强制 80% 会逼出无意义测试（凑数反模式）；报告先行让基线
数据从第一天积累，阻断绑定首个真实业务代码。

**Q3 覆盖率服务：A. 本地阈值脚本（★推荐） B. codecov 类 SaaS**
推荐 B 的历史趋势图有价值但非必需；A 零外部依赖、私有仓代码不出境，趋势数据
以 CI artifact 保留，Stage 2 若需要看板再评估。

**Q4 race detector：A. CI 默认开启（★推荐） B. 仅 nightly**
推荐 A：并发是 Go 后端核心风险（development-standards §2 专列），nightly 发现=
问题已合并；单测规模下 race 开销可接受（§6 有降级预案）。

**Q5 前端口径：A. 仅 .ts 逻辑层，.tsx 排除（★推荐） B. 全量含组件**
推荐 A：规则 4 已定"UI 组件不计入强制指标"；B 会催生大量低价值渲染快照测试。
配套".tsx 不写复杂逻辑"约定防口径套利。

## §9 实施计划

| 步骤 | 内容 | 估时 |
|---|---|---|
| 1 | D1 Go 链 + coverage-gate.sh + T2/T3/T4 | 0.5 天 |
| 2 | D2 Python 链 + T5 | 0.25 天 |
| 3 | D3 前端链 + T6 | 0.25 天 |
| 4 | D4 CI 接入 + D5 样例 + T1/T7 + DoD 收尾 | 0.5 天 |

总计 1.5 天。

## §10 后续 spec 关联

- spec-0.5：集成测试复用 pytest/go test 骨架，覆盖率口径不含集成用例（口径独立）；
- spec-1.1：合并时打开阻断开关（本 spec 预埋单变量）；
- 全部业务 spec：§4 测试用例章节按 §2.2 约定书写；
- spec-1.13/1.14：前端"逻辑入 .ts"约定作为组件设计输入。
