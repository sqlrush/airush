# spec-0.12 Stage 0 验收

> **approved & shipped** — user 验收签署 2026-08-10（自动项 7/7 + 手工项全勾；原门槛：**验收结论本身必须
> user 亲自确认**，见 §8 Q2——这是 Stage 1 的开工门槛，无预批例外）

## Header / 元数据

- **位置**：Stage 0 收官功能点；前置 spec-0.1 ~ 0.11 全部实施完成；
- **配套规则**：CLAUDE.md 规则 3（Stage N 验收未过不得开做 Stage N+1；完成写
  retrospective）、roadmap §2.2（Stage 0 验收标准五条）；
- **决策日期**：2026-08-09。

## §1 范围

### §1.1 包含（Deliverables）

| # | Deliverable | 文件清单 | 估算 | 说明 |
|---|---|---|---|---|
| D1 | 自动化验收脚本 | `deploy/scripts/stage0-acceptance.sh`：§2.1 全部可自动项逐项执行，输出 PASS/FAIL 汇总表，任一 FAIL 退出非零 | 1 文件 ~150 行 | 负向自测见 §6 |
| D2 | 验收清单文档 | `docs/stage-0-acceptance.md`：自动项引用脚本 + 手工项（CI 截图、分支保护实测、Grafana 三信号截图）+ 签署栏（user 确认） | 1 文件 ~80 行 | 验收的 SSOT |
| D3 | Stage 0 回顾 | `docs/stage-0-retrospective.md`：估时 vs 实际、偏差原因、欠账清单（分级）、纪律/规则修订建议——四节必填 | 1 文件 ~100 行 | 规则 3 义务 |
| D4 | 基线数据归档 | CI 冷/热时长、三类镜像体积、dev-up 端到端耗时、单测/集成测试时长——落 retrospective 附录 | 并入 D3 | Stage 1 回归对比基准 |
| D5 | 收官动作 | `v0.1.0-rc.1` 验收演练 → 验收签署 → `v0.1.0` 正式（spec-0.11 链路首用）；roadmap 进度表 Stage 0 全 spec 置 shipped；CHANGELOG 落段 | 流程执行 | — |

### §1.2 不包含

| 不做的事 | 理由 |
|---|---|
| 重跑 0.1-0.11 每条 DoD | 各 spec 合并时 DoD 已验；验收做端到端链路 + 抽查，重复全量是形式主义 |
| 性能压测 | 无业务负载可压；性能基线属 Stage 1（spec-1.16）职责 |
| 安全渗透/扫描加固验收 | spec-2.9 专项；Stage 0 仅确认 secret 扫描与安全 lint 在岗 |
| 多环境验证（云 k8s） | Stage 0 交付物定义就是本地 kind（roadmap §1.1）；云环境随 Stage 1 部署 spec |
| 补做 Stage 0 范围外的欠账 | 欠账走 §8 Q3 分级机制，P2 类进 backlog 不阻塞验收 |

### §1.3 例外说明

无偏离。

## §2 接口设计

### §2.1 验收项展开（roadmap §2.2 五条 → 可执行断言）

| # | roadmap 条目 | 验证方式 | 自动/手工 |
|---|---|---|---|
| A1 | 三语言最小包可构建/测试/lint | `make build && make cover && make lint` 全绿 | 自动 |
| A2 | PR 必须 CI 全绿（分支保护生效） | 直接 push main 被拒 + 红 PR 不可合并（实测记录） | 手工 |
| A3 | `make dev-up` 一键 kind + hello-world | dev-up 后全 Pod Ready + /healthz 200 | 自动 |
| A4 | 日志/指标/trace 三信号可见 | /demo 请求 → 三数据源查询到同 trace_id（0.9 T8 脚本） | 自动+截图 |
| A5 | PG 迁移框架跑通首个 migration | 集群内 `console migrate version` = 最新 + RLS 断言用例绿 | 自动 |
| A6* | 发布链路可用（本 spec 增补） | rc → 正式 v0.1.0 walkthrough 产物齐全 | 自动+手工 |
| A7* | 全部 Stage 0 spec 状态一致 | specs/README + roadmap 进度表无 DRAFT 遗留（除本 spec 验收前） | 自动 |

*A6/A7 为本 spec 对 roadmap 清单的增补，随验收文档定版。

### §2.2 欠账分级（定版）

- **P0**（安全/纪律违背）：验收前必须清零；
- **P1**（影响 Stage 1 开工的功能缺口）：验收前清零或降级为 P2 需 user 批准；
- **P2**（体验/优化类）：登记 retrospective 欠账清单 + roadmap backlog，不阻塞。

## §3 行为契约

- 验收脚本幂等可重跑；环境残留（上次 dev-up）自动清理后重建，保证"从零"语义；
- 任何 FAIL 都指向具体 spec 与修复入口（输出含 spec 编号）；
- 验收通过的定义：A1-A7 全 PASS + 手工项全勾 + **user 在 D2 签署栏确认**；
- 验收不过：修复后整体重跑（不允许"上次过的项跳过"——环境状态可能被修复改变）。

## §4 测试用例

| # | 用例 | 目的 |
|---|---|---|
| T1 | 全新机器（容器模拟）从 clone 到验收脚本全 PASS | 端到端从零可复现 |
| T2 | 故意破坏 A3（删 healthz 路由）→ 脚本 FAIL 并指向 spec-0.9/0.10 | 脚本负向有效性 |
| T3 | 故意留一个 DRAFT spec → A7 FAIL | 状态一致性检查有效 |
| T4 | 验收脚本连续两次运行结果一致 | 幂等 |
| T5 | rc→正式发布 walkthrough（复用 0.11 T1 但在验收环境执行） | 收官链路 |

## §5 与现有代码的 contract

- 新增：验收脚本、验收文档、retrospective；
- 修改：roadmap 进度表、specs/README、CHANGELOG（落段）；
- 不动：全部功能代码——验收期发现的缺陷回对应 spec 修复，本 spec 不携带功能变更；
- 对后续的接口：D3 基线数据是 spec-1.16 回归对比的基准；验收文档格式被 1.16/2.10
  等后续验收 spec 复用。

## §6 风险

| 风险 | 概率 | 缓解 |
|---|---|---|
| 验收脚本假绿（断言写错方向） | 中 | T2/T3 负向自测强制先行；脚本 review 按"每断言配一个已知失败场景"核对 |
| Stage 0 实际进度超 6 周预算 | 中 | D4 基线含估时偏差数据，roadmap §7 触发重估流程（落后 1 milestone 回 0.2 节） |
| retrospective 写成流水账 | 中 | D3 四节必填模板 + "规则修订建议"节至少 1 条实质内容（没有=没认真回顾） |
| 欠账分级被滥用（都标 P2 混过验收） | 低 | P1→P2 降级须 user 批准（§2.2），审计留痕于 retrospective |
| kind 环境差异导致 user 复验不一致 | 低 | 验收脚本输出环境指纹（docker/kind/helm 版本），复验前比对 |

## §7 DoD

- [ ] D1-D5 全部完成，T1-T5 通过
- [ ] A1-A7 全 PASS 的完整输出归档（附截图的手工项在 D2 勾选）
- [ ] **user 签署验收确认**（D2 签署栏）
- [ ] v0.1.0 正式发布产物齐全（GH Release + 镜像 + chart）
- [ ] retrospective 四节完整且含 ≥1 条规则修订建议（或显式"无修订"论证）
- [ ] 基线数据表完整（D4 五项指标）
- [ ] 欠账清单分级完毕，P0/P1 清零或有 user 批准的降级记录
- [ ] roadmap 进度表 Stage 0 全部置 shipped、Stage 1 起始状态标注
- [ ] CHANGELOG v0.1.0 落段
- [ ] commit 格式合规，独立 commit 序列

## §8 Q&A

**Q1 验收形式：A. 脚本自动化 + 手工清单混合（★推荐） B. 纯手工 checklist**
推荐 A：可自动的七成项目脚本化后验收可重复（Stage 1 回归还要用）；纯手工每次
验收成本高且易漏项。不可自动化的（分支保护实测、截图）保留手工，不硬凑。

**Q2 验收确认人：A. user 亲自签署（★推荐） B. 自验通过即视为过**
推荐 A：Stage 验收是规则 3 的跨 Stage 硬门槛，与"spec approve 是编码门槛"同级——
自验自批会让分级预批制失去末端收口。签署形式就是 D2 文档勾选 + 一句确认。

**Q3 欠账处理：A. 分级（P0/P1 清零，P2 入 backlog）（★推荐） B. 全部清零才验收**
推荐 A：B 会让验收被长尾优化项拖死，违背"端到端优先"策略；分级让阻塞判断
显式化且降级需 user 批准，不留暗门。

**Q4 v0.1.0 时机：A. 验收通过即发布（★推荐） B. 攒到 Stage 1 首个功能一起**
推荐 A：发布链路本身是 Stage 0 交付物（spec-0.11），不发布=链路未验收；
且 v0.1.0 给 Stage 1 提供明确的回归基线锚点。

## §9 实施计划

| 步骤 | 内容 | 估时 |
|---|---|---|
| 1 | D1 脚本 + T2/T3 负向自测 + T4 | 0.5 天 |
| 2 | D2 清单文档 + T1 全新环境跑通 | 0.25 天 |
| 3 | D3/D4 retrospective + 基线归档 | 0.25 天 |
| 4 | D5 收官（rc→正式、状态同步）+ user 验收会 | 0.5 天 |

总计 1.5 天（不含验收发现缺陷的修复时间——回各 spec 计）。

## §10 后续 spec 关联

- Stage 1 全部 spec：验收通过是其开工前提（规则 3）；
- spec-1.16：复用 D1 脚本模式 + D4 基线做回归对比；
- CLAUDE.md：retrospective 的规则修订建议若被 user 采纳，按其流程修订纪律文档；
- spec-0.11：本 spec 是 release 链路的首个真实用户（rc→正式全流程）。
