# spec-0.11 版本号与 release 节奏

> **approved & shipped** — Stage 0 验收签署 2026-08-10（分级预批流程，spec-0.12）

## Header / 元数据

- **位置**：Stage 0 第 11 个功能点；前置 spec-0.3（CI）、spec-0.10（镜像/chart 产物形态）；
- **配套规则**：CLAUDE.md 规则 7（编码完成更新 CHANGELOG）、5 阶段流程第 5 步
  （Release & CI/CD）；development-standards §6（commit 中文描述——影响自动化选型，见 §8 Q2）；
- **决策日期**：2026-08-09。

## §1 范围

### §1.1 包含（Deliverables）

| # | Deliverable | 文件清单 | 估算 | 说明 |
|---|---|---|---|---|
| D1 | 版本策略定版 | 本 spec §2.1（semver 语义、统一平台版本、节奏、rc 通道），并摘录进 README | 文档 ~40 行 | §8 Q1/Q3/Q5 |
| D2 | CHANGELOG 初始化 | `CHANGELOG.md`（keep-a-changelog 格式，Unreleased 段就位）+ spec-0.3 PR 模板增"已更新 CHANGELOG"检查项 | 2 文件 ~40 行 | §8 Q2 |
| D3 | release workflow | `.github/workflows/release.yml`：`v*` tag 触发 → 全量校验（同 CI）→ 镜像补版本 tag → `helm package`（chart version=app version）→ GitHub Release 附 chart 包与 CHANGELOG 节选 | 1 文件 ~90 行 | 复用 0.3 缓存与 job |
| D4 | release 准备脚本 | `deploy/scripts/release-prep.sh`：校验 Unreleased 非空、版本号递增合法、把 Unreleased 落段为版本号、输出 release notes | 1 文件 ~70 行 | 人触发，防手误 |
| D5 | 版本一致性断言 | release workflow 内校验：git tag = 二进制 `--version` = chart version = 镜像 tag 四方一致，任一不符即中止 | 并入 D3 | 防"版本漂移"事故 |

### §1.2 不包含

| 不做的事 | 理由 |
|---|---|
| 组件独立版本号 | §8 Q1 决策统一平台版本；独立版本的兼容矩阵管理成本远超单体发布收益 |
| semantic-release/release-please 自动化 | commit description 为中文（standards §6），conventional-commits 解析链路不匹配；且发布节奏由 Stage 驱动而非 commit 驱动 |
| 镜像仓库保留/清理策略 | ghcr 私有镜像量级尚小；量变后运维专项处理 |
| API 版本管理 | `/api/v1` 契约版本与产品版本正交（standards §5 已定），互不联动，在此显式声明防混淆 |
| 私有化交付版本通道 | spec-4.5 按客户交付节奏另立（可能滞后主线若干版本） |
| 发布公告/release notes 对外渠道 | 无外部用户；Stage 2 可售后随商业化补 |

### §1.3 例外说明

无偏离。

## §2 接口设计

### §2.1 版本策略（定版）

- 形态：`v<major>.<minor>.<patch>[-rc.N]`，全平台（四 Go 服务、skills、frontend、chart）
  共享同一版本号；
- pre-1.0 节奏：**minor = Stage 完成或跨 spec 的重要功能组**（如 Stage 1 验收 → v0.2.0）；
  **patch = 修复与小improvement**；**v1.0.0 = GA（Stage 4 验收）**；
- rc 通道：Stage 验收期打 `-rc.N` 供验收演练，验收通过后同内容打正式号；
  rc 镜像 tag 带 `-rc` 后缀，Helm values 默认值禁引用 rc；
- tag 不可变：打错不删除不移动，用下一个 patch 修正（追溯性优先）；
- Stage 0 完成 = `v0.1.0`（首个有版本的产物）。

### §2.2 CHANGELOG 约定（定版）

- keep-a-changelog 结构（Added/Changed/Fixed/Security 小节），条目中文；
- 每个改变行为的 PR 必须在 Unreleased 段追加条目（PR 模板检查项）；纯 docs/测试 PR 豁免；
- release-prep 时 Unreleased 落段为版本 + 日期，生成 notes——**CHANGELOG 是 release notes
  的唯一来源**，不从 commit log 生成。

## §3 行为契约

- release workflow 仅由 `v*` tag 触发，且 tag 必须指向 main 上已全绿的 commit（校验失败中止）；
- 发布产物四方版本一致（D5），否则发布失败且无部分产物残留（镜像 tag 推送放最后一步）；
- 同一 tag 重跑 workflow 幂等（产物覆盖同内容）；
- CHANGELOG Unreleased 为空时 release-prep 拒绝执行（防空发布）。

## §4 测试用例

| # | 用例 | 目的 |
|---|---|---|
| T1 | 完整 walkthrough：release-prep → tag → workflow → GH Release 产物齐全 | 主链路 |
| T2 | tag 指向非全绿 commit → 中止 | 质量门闩 |
| T3 | 手工制造版本不一致（改 ldflags）→ D5 断言中止 | 一致性 |
| T4 | Unreleased 为空跑 release-prep → 拒绝 | 防空发布 |
| T5 | rc tag → 产物带 rc 标记、GH Release 标 pre-release | rc 通道 |
| T6 | 同 tag 重跑 → 幂等 | 可重入 |
| T7 | 版本号跳号/回退 → release-prep 校验拒绝 | 递增合法性 |

## §5 与现有代码的 contract

- 新增：CHANGELOG.md、release.yml、release-prep.sh；
- 修改：PR 模板（0.3 D6 增检查项）、README（版本策略摘录）；
- 不动：CI 主 workflow、镜像构建逻辑（复用 make images）；
- 对后续 spec 的接口：§2.1 节奏定版；Stage 验收 spec（0.12/1.16/…）的产出动作含
  "打对应版本 tag"。

## §6 风险

| 风险 | 概率 | 缓解 |
|---|---|---|
| 手工 CHANGELOG 遗漏条目 | 高 | PR 模板检查项 + review 核对；release-prep 输出 diff 供发布前人工过目 |
| 统一版本让未变更组件也重发镜像 | 确定发生 | 接受：同 sha 层缓存使成本≈0，换来的部署一致性（单版本号定位全站）是运维刚需 |
| rc 产物被误当正式版部署 | 低 | 镜像 tag 显式 -rc + GH pre-release 标记 + values 禁 rc 校验（helm lint 规则） |
| chart 结构破坏性变更与 app 版本语义耦合 | 低 | 约定：chart 破坏性变更也 bump minor 并在 CHANGELOG Changed 标注 `[chart]` 前缀 |
| tag 权限失控（任何人可发版） | 低 | 分支保护 + tag 保护规则（v* 仅 admin），随 0.3 D3 脚本一并配置 |

## §7 DoD

- [ ] D1-D5 就位，T1-T7 全部通过（walkthrough 记录附 PR）
- [ ] CHANGELOG 含 Stage 0 期间已合并内容的补录（从 git log 人工整理一次性欠账）
- [ ] tag 保护规则生效（v* 仅维护者）
- [ ] README 版本策略一节与 §2.1 一致
- [ ] PR 模板检查项上线
- [ ] rc 全链路演示一次（T5 记录）
- [ ] 四方一致性断言脚本有单测（版本比较逻辑）
- [ ] 发布失败无部分产物残留验证（T3 后检查 ghcr）
- [ ] specs/README.md 与 roadmap 进度表更新
- [ ] commit 格式合规，独立 commit 序列

## §8 Q&A

**Q1 版本粒度：A. 平台统一版本（★推荐） B. 组件独立版本**
推荐 A：单体发布节奏 + "一个号定位全站状态"对运维/支持/私有化交付都是刚需；
B 的兼容矩阵（哪个 console 配哪个 gateway）在 1-2 人团队是纯负担。UI 里
"平台版本 0.9.2"的展示语义也依赖统一版本。

**Q2 CHANGELOG：A. PR 随手维护 Unreleased（★推荐） B. release-please 类自动生成**
推荐 A：中文 commit 与 conventional 解析不匹配；且"人写给人看的变更"质量
天然高于 commit 罗列。遗漏风险由模板检查项缓解（§6）。

**Q3 节奏：A. minor=Stage/重要功能、1.0=GA（★推荐） B. 日历版本（CalVer）**
推荐 A：产品处于能力驱动阶段，版本号应传达能力边界；CalVer 适合恒定节奏发布的
成熟品，现在采用会让 0.x 语义（不稳定 API）丢失。

**Q4 chart 版本：A. 与 app 同号联动（★推荐） B. 独立演进**
推荐 A：部署单元=全平台，双版本号必然出现"chart 1.3 装 app 0.9"组合爆炸；
chart 自身破坏性变更按 §6 约定处理。

**Q5 rc 通道：A. Stage 验收期使用（★推荐） B. 不用 rc 直接正式号**
推荐 A：验收演练（0.12/1.16）需要可指认的候选产物，验收不过重打 rc.N+1 不污染
正式号序列；B 会让验收失败留下废弃正式版本号。

## §9 实施计划

| 步骤 | 内容 | 估时 |
|---|---|---|
| 1 | D1/D2 策略 + CHANGELOG + 模板 | 0.25 天 |
| 2 | D3/D5 release workflow + 一致性断言 + T1/T2/T3/T5/T6 | 0.5 天 |
| 3 | D4 release-prep + T4/T7 + DoD 收尾 | 0.25 天 |

总计 1 天。

## §10 后续 spec 关联

- spec-0.12：Stage 0 验收通过 → 按本 spec 打 `v0.1.0`（rc 演练在前）；
- spec-1.16：Stage 1 验收 → `v0.2.0`，性能基线随 Release 归档；
- spec-4.1（计费）/4.5（私有化）：版本号进入商业合同语境，届时评估是否提前 1.0；
- 全部 spec：release 动作是 5 阶段流程第 5 步的机器面，规则 7 的 CHANGELOG 义务由
  PR 模板承载。
