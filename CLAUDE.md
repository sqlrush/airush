# AIRush 项目指令（Claude 必读）

> 本文件是本项目的研发纪律。与全局规则（~/.claude/rules/common/*.md）叠加生效；
> 冲突时以本文件为准。参照 pgrac 项目纪律体系建立，按本项目（SaaS 多租户平台）裁剪。

## 文档权威链（Single Source of Truth）

```
docs/2026-08-08-airush-platform-design.md   ← 架构决策 AD-1..AD-10 的 SSOT
docs/development-roadmap.md                  ← Stage 划分与 spec 清单的 SSOT
specs/spec-N.M-<slug>.md                     ← 单功能点实现契约的 SSOT
```

- 按 spec 编码；spec 与设计文档冲突时，**停止编码**，先修订上游文档并获 user approve；
- 禁止"代码先这么写，文档以后改"；
- 架构决策（AD）变更必须先改设计文档的 AD 表并记录修订历史。

## 核心安全原则（最高优先级，任何 spec 不得违反）

1. **凭据边界（AD-4）**：数据库凭据只存客户侧 Connector；平台侧代码、镜像、日志、
   LLM prompt 中出现客户数据库凭据 = P0 事故；
2. **租户边界（AD-10）**：控制面所有租户数据表必须带 tenant_id + RLS；
   禁止仅靠应用层 WHERE 过滤实现隔离；跨租户语义的任何变更是硬门槛（见规则 5）；
3. **数据边界（AD-3）**：客户原始数据默认不出内网；平台侧 skill 只处理已脱敏的结构化数据；
   任何把"平台侧直连客户数据库"作为便捷路径的设计必须被拒绝；
4. **操作边界（AD-9）**：动作类指令必须走审批 + 一次性令牌 + 双层白名单；
   Connector 受控执行器禁止接受任意 SQL / 任意 shell；
5. secrets 一律走环境变量 / secret 管理，启动时校验存在性，永不入 git（全局 security.md 同样适用）。

## 规则 1：单功能点研发 5 阶段流程（强制）

```
SPEC（编码前讨论） → TDD 编码 → 集成测试 → Code Review → Release & CI/CD
```

**禁止**：跳过 spec 直接编码；跳过 TDD 直接写实现；"先把功能写完，测试以后补"；"review 等等再说"。

**user approve spec 是进入编码的硬门槛。**

## 规则 2：SPEC 结构与详细度

- 命名：`specs/spec-<stage>.<序号>-<slug>.md`；模板结构见 roadmap 第 6 节（10 节必含）；
- 量化门槛（不达标 = spec 没准备好，退回补全）：

| 指标 | 最低值 |
|---|---|
| §1 Deliverable 数（含文件清单 + LOC 估算） | ≥ 3 |
| §1 "不包含"表条数（每条带理由） | ≥ 4 |
| §6 风险条数（含概率 + 缓解，禁泛泛之词） | ≥ 5 |
| §7 DoD 条数 | ≥ 10 |
| §8 Q&A 数（每个 ≥2 选项 + ★推荐 + 理由） | ≥ 4 |

- 流程：起草自检达标 → **立即 commit + push（顶部标 `DRAFT — 待 user approve`）** → user 评审
  → approve 后删 DRAFT 标记加 approve 日期 → spec frozen；后续修订只能追加 changelog 不重写；
- `spec-1.2-connector-core` 起草后将被指定为参照模板（对照结构与颗粒度，不是抄内容）；
- 反模式禁止：Q&A 单选项、Deliverable 无 LOC 估算、风险写"小心 bug"、"不包含"不给理由。

## 规则 3：Stage 节奏

- Stage 顺序与清单见 roadmap；**禁止跨 Stage 跳跃**（Stage N 验收未过不得开做 Stage N+1）；
- 例外仅限"基础设施前置"，且须在对应 spec §1.4 例外说明中登记；
- 每 Stage 完成写 `docs/stage-N-retrospective.md`；
- spec 状态变更（DRAFT/frozen/shipped）当日同步 roadmap 第 8 节进度表。

## 规则 4：测试与质量

- 单元测试覆盖率 ≥ **80%**；集成测试覆盖核心路径；每个错误码有触发用例；
- CI 全绿才能合并 main；lint、安全扫描必须通过；分支保护开启后禁止绕过；
- 性能敏感路径（采集上报、接入网关、LLM 网关）每 Stage 留基线数据，回归 >5% 必须查根因；
- 测试红线：禁止为过 CI 注释/跳过失败用例；skip 必须带 issue 链接与理由。

## 规则 5：approved spec 后默认持续编码

user approve spec 后（或明确说"继续/直接改"后），默认**持续实现到当前 Deliverable 完成**，
不把低风险实现细节升级成用户决策：spec 内已有推荐项、命名、测试组织、观测性细节、
P1/P2 obvious fix，直接按推荐或既有惯例实现，在进度报告/commit message 里说明即可。

**必须停下来找 user 决策的硬门槛**（其余默认继续）：

1. 架构 / scope / 验收标准改变，或需要新增/修改设计文档 AD；
2. 触碰核心安全原则（凭据/租户/数据/操作边界）且没有唯一安全的 fail-closed 修法；
3. 持久化 schema、对外 API contract、Connector 协议等兼容性契约发生非 spec 既定变化；
4. 新增第三方依赖、网络/系统服务，或任何破坏性操作（删库、force push、删除用户文件）。

## 规则 6：实现完整性

**要么完整实现，要么显式拒绝**——禁止半实现/占位代码。
未实现的分支必须显式报错（含错误码），禁止静默返回假数据；
禁止 `TODO: 以后处理错误` 式吞错（全局 coding-style.md 错误处理条款同样适用）。

## 规则 7：文档同步

编码中遇到设计盲区：**停止编码** → 修订对应 spec/设计文档 → user approve → 再继续。
编码完成后：更新 spec 状态、roadmap 进度表、CHANGELOG；改动过的设计文档在 Stage 末复审一致性。

## 规则 8：代码与协作红线（摘要）

- **Git**：commit 格式 `<type>: <description>`（feat/fix/refactor/docs/test/chore/perf/ci）；
  禁止 force push main；禁止把未 review 的大杂烩一次性 commit；
- **依赖（分层管控，2026-08-09 user 定）**：
  - 后端直接依赖（Go module / PyPI）：事前审批（规则 5 硬门槛）+ 选型理由——量少且在核心路径，维持严格；
  - 前端直接依赖（npm dependencies）：**备案制**——PR 描述中列出新增依赖 + 一句话理由，review 时核查，无需事前等待批复；
  - 传递依赖：不做人工审批——lockfile 锁版本 + CI 安全扫描（audit 类工具），出 CVE 才升级人工处理；
  - devDependencies（构建期、不进产物）：豁免备案，仅受 CI 扫描覆盖；
- **多语言 lint**：Go=golangci-lint、Python=ruff+mypy、TS=eslint+prettier，配置以 spec-0.2 为准；
- **代码风格**：遵循全局 coding-style.md（不可变数据、小函数 <50 行、文件 <800 行、按领域组织）；
- **可观测性**：新增服务必须接入 spec-0.9 三件套（日志/metrics/tracing）后才算完成；
- **k8s**：所有组件经 Helm 部署，禁止 kubectl 手工改线上资源作为"临时方案"。
