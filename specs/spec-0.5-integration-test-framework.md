# spec-0.5 集成测试框架

> **DRAFT — 待 user approve**（Stage 0 分级预批：push 即可开工，user 异议后修改）

## Header / 元数据

- **位置**：Stage 0 第 5 个功能点；前置 spec-0.1（Makefile）、spec-0.3（CI job 预留位）、
  spec-0.4（测试骨架与组织约定）；首个真实消费方是 spec-0.6（迁移框架的集成验证）；
- **关联文档**：`docs/development-standards.md` §2（Go 集成测试 build tag 约定）；
  CLAUDE.md 规则 4（集成测试覆盖核心路径）、规则 6（禁静默跳过）；
- **决策日期**：2026-08-09。

## §1 范围

### §1.1 包含（Deliverables）

| # | Deliverable | 文件清单 | 估算 | 说明 |
|---|---|---|---|---|
| D1 | 手工调试依赖栈 | `deploy/compose/dev-deps.yml`（PG 16 + Redis 7，healthcheck、固定版本 tag）+ `make dev-deps-up/down` | 1 文件 ~60 行 | 仅供人肉调试长驻；自动化测试不连它（§8 Q1） |
| D2 | Go 集成测试基建 | `testkit/` Go module：testcontainers 封装（PG/Redis 容器启动、连接串注入、随机 schema 创建）；`//go:build integration` 约定 | ~4 文件 ~200 LOC | 共享测试基建例外见 §1.3 |
| D3 | Python 集成测试基建 | `skills/tests/conftest.py` 增 testcontainers fixture + `integration` marker；`-m integration` 才执行 | ~2 文件 ~80 LOC | pytest 骨架复用 spec-0.4 |
| D4 | 统一入口与 CI 接入 | `make integration-test`（三语言聚合）；spec-0.3 预留的 `ci/integration` job 激活（PR 增量 + main 全量） | ~30 行增量 | 阻断级 check（§8 Q2） |
| D5 | 端到端冒烟用例 | Go：起 PG 容器→建 schema→读写断言；Python：起 Redis 容器→读写断言 | 2 文件 ~90 LOC | 证明框架闭环，兼作范本 |

### §1.2 不包含

| 不做的事 | 理由 |
|---|---|
| kind/k8s 级验证 | spec-0.12 验收专项；集成测试面向"组件×真实依赖"，不面向部署形态 |
| 跨服务端到端链路（Connector→网关→落库） | 依赖 Stage 1 真实服务，spec-1.5/1.16 各自定义 |
| LLM mock 服务 | spec-1.7（LLM 网关）按其接口形态自定，现在定会返工 |
| MySQL/TimescaleDB 容器封装 | 首个消费方分别是 spec-1.3/1.5，届时向 testkit 增量添加，避免囤积无人用的封装 |
| 集成测试覆盖率闸门 | §8 Q5：集成用例不进覆盖率分子分母，口径独立（单测闸门已由 spec-0.4 承担） |
| 性能/压测框架 | 规则 4 性能基线绑定各性能敏感 spec，工具形态届时定 |

### §1.3 例外说明

D2 建立共享 `testkit/` module，**偏离 spec-0.1 "出现第二个使用者前不抽公共库"**：
测试容器封装天然被 console/gateway/agent-runtime 多方使用（0.6 起即有两个消费方），
且仅测试依赖、不进产物——依据该条款的立法本意（防过早抽象业务库）判定不冲突，
在此显式登记。

## §2 接口设计

### §2.1 双轨约定（定版）

| 轨道 | 用途 | 生命周期 |
|---|---|---|
| testcontainers（D2/D3） | 全部自动化集成测试 | 测试进程自管：起容器→跑→销毁（ryuk 兜底回收） |
| compose dev-deps（D1） | 人肉调试、psql 手工连库 | `make dev-deps-up` 长驻，与测试互不感知 |

**禁止**自动化测试连接 compose 长驻实例（测试间状态污染 + CI 不可复现）。

### §2.2 关键约定

- Go 集成文件：`*_integration_test.go` + `//go:build integration`，`make test` 不触发；
- Python：`@pytest.mark.integration`，默认 collection 排除；
- 数据隔离：**容器 per-package 复用 + schema per-test 随机命名**（`t_<随机8位>`），
  测试结束 drop schema；用例间禁止共享可变数据；
- 无 docker 环境：`make integration-test` 显式报错退出（规则 6 禁静默 skip），
  错误信息附安装指引；
- 容器镜像版本与生产目标版本一致（PG 16/Redis 7），升级走 PR 修订本 spec。

## §3 行为契约

- `make test` 与 `make integration-test` 严格分离：前者无 docker 依赖，后者必须 docker；
- 集成用例失败输出容器日志尾部 50 行（可定位性）；
- 同一用例连续运行 3 次结果一致（无顺序依赖、无残留状态）；
- testkit API 破坏性变更需检查全部消费方（go.work 内编译即验证）。

## §4 测试用例

| # | 用例 | 目的 |
|---|---|---|
| T1 | D5 冒烟用例经 `make integration-test` 通过 | 框架闭环 |
| T2 | `make test` 不执行任何集成用例（计数为 0） | build tag/marker 隔离有效 |
| T3 | 无 docker 环境执行 → 显式报错含指引（容器内模拟） | 禁静默跳过 |
| T4 | 两个并行用例各自 schema 互不可见 | 数据隔离 |
| T5 | 同一用例连跑 3 次全绿 | 可重复性 |
| T6 | 用例失败时输出含容器日志尾部 | 可定位性 |
| T7 | CI `ci/integration` 在 PR 上运行并可阻断合并 | 闸门接入 |

## §5 与现有代码的 contract

- 新增：`testkit/`（入 go.work）、`deploy/compose/dev-deps.yml`、conftest 增量、冒烟用例；
- 修改：`Makefile`（integration-test 目标）、spec-0.3 workflow（激活预留 job）；
- 不动：单测链路（spec-0.4 全部契约）、lint 配置（testkit 纳入 lint 范围）；
- 对后续 spec 的接口：testkit 容器封装 API + §2.2 隔离约定为全部集成用例的基线。

## §6 风险

| 风险 | 概率 | 缓解 |
|---|---|---|
| CI 上容器镜像拉取慢/限流（Docker Hub） | 中 | 镜像固定 digest + GHA registry cache；持续超时则镜像转存 ghcr |
| testcontainers-python 维护质量低于 Go 版 | 中 | Python 侧仅用最基础的容器起停 API；异常时降级为 Go 版起容器 + 连接串注入 Python 测试 |
| 容器复用（per-package）残留状态导致 flaky | 中 | schema per-test 硬约定 + T4/T5 守护；发现共享状态用例按测试红线处理 |
| CI 时长膨胀（集成用例逐 Stage 增多） | 高 | PR 按 path filter 增量跑；单包集成时长 >3 分钟时该包必须拆分或降级到 main-only（届时修订） |
| 本地 mac（用户环境）docker 引擎差异（OrbStack） | 低 | testcontainers 走标准 socket 探测；README 记录已验证引擎 |

## §7 DoD

- [ ] D1-D5 就位，T1-T7 通过（记录附 PR）
- [ ] testkit 通过 lint 与单测（自身逻辑有单测）
- [ ] compose dev-deps 与 testcontainers 版本对齐（同 PG/Redis 版本）
- [ ] `ci/integration` 成为 PR 阻断 check（分支保护更新，重放 D3 脚本）
- [ ] 冒烟用例符合 spec-0.4 §2.2 组织约定
- [ ] 无 docker 报错信息含安装指引（T3 输出附 PR）
- [ ] 集成用例不影响覆盖率闸门数字（口径验证）
- [ ] README 增"运行集成测试"一节
- [ ] specs/README.md 与 roadmap 进度表更新
- [ ] commit 格式合规，独立 commit 序列

## §8 Q&A

**Q1 环境形态：A. testcontainers 自管（★推荐） B. 测试连入固定 compose 环境**
推荐 A：自管生命周期 = CI/本地行为一致 + 无状态污染；B 少一次容器启动但引入
"谁先起环境"的隐式依赖，flaky 温床。compose 保留为人肉调试轨（§2.1）。

**Q2 CI 时机：A. PR 阻断（path filter 增量）+ main 全量（★推荐） B. 仅 nightly**
推荐 A：集成缺陷 nightly 才发现=已污染 main，违背"CI 全绿合并"纪律；
时长风险由增量执行 + §6 时长红线控制。

**Q3 共享测试基建：A. 独立 testkit module（★推荐） B. 各 Go 模块各写一份**
推荐 A：容器封装含超时/重试/日志导出等易错细节，复制会漂移；仅测试依赖不进产物，
§1.3 已登记与 spec-0.1 条款的关系。

**Q4 数据隔离：A. 容器 per-package + schema per-test（★推荐） B. truncate 清理 C. 容器 per-test**
推荐 A：容器启动秒级、schema 创建毫秒级，A 在速度与隔离间最优；B 依赖清理逻辑
正确性（漏表即污染）；C 最干净但用例数×启动时间不可扩展。

**Q5 集成覆盖率：A. 不计入闸门（★推荐） B. 与单测合并统计**
推荐 A：合并统计会让"集成跑一遍拉高数字"掩盖单测缺失，规则 4 的 80% 指的是
单元层；集成质量由"核心路径有用例"在各 spec §4 清单保证。

## §9 实施计划

| 步骤 | 内容 | 估时 |
|---|---|---|
| 1 | D2 testkit + Go 冒烟 + T2/T4/T5 | 0.5 天 |
| 2 | D3 Python 基建 + Redis 冒烟 | 0.25 天 |
| 3 | D1 compose + D4 CI 接入 + T3/T6/T7 | 0.5 天 |
| 4 | DoD 收尾 + README | 0.25 天 |

总计 1.5 天。

## §10 后续 spec 关联

- spec-0.6：迁移框架的集成验证是 testkit 首个真实消费方（起 PG→跑 migration→断言 schema）；
- spec-1.3/1.5：MySQL/TimescaleDB 容器封装届时增量入 testkit；
- spec-1.16：端到端验收复用集成框架的容器编排经验但独立成篇；
- spec-0.12：kind 验收与本框架互补（部署形态 vs 依赖交互），不重叠。
