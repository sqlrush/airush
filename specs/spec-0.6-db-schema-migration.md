# spec-0.6 控制面数据库 schema 与迁移框架

> **approved & shipped** — Stage 0 验收签署 2026-08-10（分级预批流程，spec-0.12）

## Header / 元数据

- **位置**：Stage 0 第 6 个功能点；前置 spec-0.5（testkit 起 PG 做集成验证）；
  为 spec-1.1（领域表）、spec-1.8（threadstore）、spec-2.1（RLS 全面启用）奠定约定；
- **安全权重**：本 spec 落地 **AD-10 租户边界**的存储层基建——CLAUDE.md 核心安全原则 2
  （租户表必须 tenant_id + RLS，禁止仅靠应用层 WHERE），任何后续 spec 不得违反；
- **关联文档**：设计文档 §AD-10、`docs/development-standards.md` §5（租户永不出现在 API 路径）；
- **决策日期**：2026-08-09；
- **评审点（2026-08-10 user 定）**：本 spec D4 首批迁移的表结构，以及后续一切 PG 建模
  （spec-1.1 领域表、spec-1.8 threadstore 会话/上下文表等），**实施前须与 user 逐表过
  一遍设计**——此评审不受 Stage 0 分级预批豁免；
- **评审记录**：2026-08-10 user 会话评审通过——tenants 表（含 slug、status 文本+CHECK、
  timestamptz/UTC、updated_at 应用层维护）与 RLS 基建（airush_app NOLOGIN、SET LOCAL、
  fail-closed）；spec-1.1 领域表（connectors/datasources/credentials/groups/aliases/agents
  简表）设计一并冻结，实施仍归 spec-1.1；
- **依赖审批**（规则 8）：Go：`golang-migrate/migrate/v4`（iofs+pgx5 驱动）；
  `jackc/pgx/v5` 已于 spec-0.5 审批。approve 本 spec 即完成审批；
- **实施修订（2026-08-10）**：§3/T8 的"校验和由工具强制"更正——golang-migrate 无内建
  校验和，迁移不可变性改由 CI git-diff 检查强制（已合并迁移文件被修改/删除即红）。

## §1 范围

### §1.1 包含（Deliverables）

| # | Deliverable | 文件清单 | 估算 | 说明 |
|---|---|---|---|---|
| D1 | 迁移工具链 | golang-migrate 以库形式嵌入：`console/cmd/console/migrate.go`（`console migrate up/down/version` 子命令，iofs 嵌入迁移文件）| ~2 文件 ~150 LOC | 执行方式见 §8 Q3 |
| D2 | 迁移脚手架与规范 | `make migrate-new name=xxx`（生成 `NNNN_xxx.up.sql/.down.sql` 空模板）+ CI 编号重复检查脚本 | ~2 文件 ~60 行 | 编号规则见 §8 Q2 |
| D3 | RLS 表设计约定 | 本 spec §2.2 SQL 模板（租户表必备四要素）+ 系统表白名单登记机制 | 文档内嵌 | 后续所有建表 spec 的强制模板 |
| D4 | 首批 migration | `console/migrations/0001_rls_foundation.up/.down.sql`：`airush_app` 非 owner 角色、`tenants` 系统表、`app.tenant_id` 会话变量约定注释 | 2 文件 ~80 行 SQL | 真实领域表留给 spec-1.1 |
| D5 | 集成验证用例 | testkit 起 PG → `up → down → up` 幂等断言；RLS 隔离断言（模板建临时租户表，租户 A 上下文看不见 B 行，owner 也被 FORCE 拦截） | ~2 文件 ~120 LOC | spec-0.5 框架首个真实消费方 |

### §1.2 不包含

| 不做的事 | 理由 |
|---|---|
| 真实领域表（datasource/agent/skill 等） | spec-1.1 按领域模型定，本 spec 只给约定与基建 |
| threadstore（threads/rollout_events）schema | spec-1.8 绑定 codexgo 抽核数据模型，届时按本 spec 模板建 |
| TimescaleDB 指标 schema | spec-1.5 专项（超表/分区/压缩策略是独立决策域） |
| 应用层租户上下文中间件（set app.tenant_id） | spec-1.1 随首个 API 落地；本 spec 用集成测试手工 set 验证机制 |
| RLS 性能优化（policy 索引策略调优） | 需真实查询形态，Stage 1 基线实测后按数据决策 |
| 备份/PITR 策略 | 生产走云托管（k8s-scaling-design §1.1），私有化在 spec-4.5 |

### §1.3 例外说明

无偏离。0001 迁移含角色创建（`CREATE ROLE`）——需集群级权限，本地/CI 容器内可行；
云托管环境角色由 IaC/手工预建，迁移中角色语句用 `DO $$ ... IF NOT EXISTS` 幂等包裹。

## §2 接口设计

### §2.1 迁移文件规范（定版）

- 路径：`console/migrations/NNNN_<snake_case>.up.sql` / `.down.sql`（NNNN 四位顺序号）；
- 每迁移单一意图；DDL 与回填数据分开两个迁移；
- down 必写：纯结构回滚；不可逆迁移（删列/删表含数据）down 中 `RAISE EXCEPTION 'irreversible'`
  并在 up 文件头注释标注 `-- IRREVERSIBLE`（§8 Q4）；
- 迁移一经合并 main 不得修改内容，修正走新迁移（校验和由工具强制）。

### §2.2 租户表模板（定版，AD-10 机器可执行面）

```sql
CREATE TABLE <name> (
  tenant_id  UUID NOT NULL REFERENCES tenants(id),
  id         UUID NOT NULL DEFAULT gen_random_uuid(),
  -- 业务列 --
  PRIMARY KEY (tenant_id, id)                     -- ① 复合主键，tenant_id 前缀
);
ALTER TABLE <name> ENABLE ROW LEVEL SECURITY;      -- ②
ALTER TABLE <name> FORCE  ROW LEVEL SECURITY;      -- ③ owner 亦不可绕过
CREATE POLICY tenant_isolation ON <name>
  USING (tenant_id = NULLIF(current_setting('app.tenant_id', true), '')::uuid);  -- ④
  -- missing_ok=true：变量从未设置返回 NULL；NULLIF 兜住连接复用后 GUC 退化为
  -- 空字符串的情形（SET LOCAL 事务结束后自定义 GUC 值为 ''，直接 ::uuid 会报错）。
  -- 两种"未设置"形态均判 false → 0 行 fail-closed（实施修订 2026-08-10，集成测试实证）
```

- 应用一律以 `airush_app`（非 owner）连接；每连接/事务先 `SET app.tenant_id`；
- **系统表白名单**：无租户语义的表（`tenants`、`schema_migrations`、未来平台级配置表）
  须在建表迁移头部注释 `-- SYSTEM TABLE (no tenant scope): <理由>`，review 按此审计；
- 违反模板的建表 SQL = 触碰核心安全原则，review 直接打回。

## §3 行为契约

- `console migrate up` 幂等：已最新时零操作退出 0；
- 迁移失败即停，不自动回滚已成功步骤（PG DDL 事务性由单文件事务保证），残留状态可用
  `version` 命令诊断；
- 多副本并发执行迁移安全（golang-migrate 咨询锁），但部署约定仍为单点执行（§8 Q3）；
- RLS 契约：未 `SET app.tenant_id` 的会话查询租户表返回 0 行（fail-closed），而非报错——
  集成用例 T5 固化此语义。

## §4 测试用例

| # | 用例 | 目的 |
|---|---|---|
| T1 | 空库 `up` → 全部对象就位 | 主链路 |
| T2 | `up → down → up` 结果一致 | 可逆性与幂等 |
| T3 | 已最新再 `up` → 零操作 | 幂等退出语义 |
| T4 | 租户 A 上下文只见 A 行（B 同理） | RLS 隔离 |
| T5 | 未 set tenant_id → 0 行 | fail-closed |
| T6 | owner 角色直查被 FORCE RLS 拦截 | 防绕过（§8 Q5） |
| T7 | 编号重复的迁移文件 → CI 检查失败 | 规范执行 |
| T8 | 修改已合并迁移内容 → 校验和不匹配报错 | 不可变性 |

## §5 与现有代码的 contract

- 新增：console 内 migrate 子命令与 migrations 目录、make 目标、CI 检查、集成用例；
- 修改：`Makefile`、testkit（如需 PG 版本参数化）；
- 不动：其余组件；
- 对后续 spec 的接口：§2.1 文件规范 + §2.2 租户表模板 + `airush_app`/`app.tenant_id`
  命名——三者定版后变更需修订本 spec 并全量评估既有迁移。

## §6 风险

| 风险 | 概率 | 缓解 |
|---|---|---|
| RLS policy 让查询计划变差（每行求值 current_setting） | 中 | 模板①保证 tenant_id 索引前缀；Stage 1 留基线，劣化 >5% 走规则 4 根因流程 |
| owner/superuser 路径绕过 RLS（人为运维操作） | 中 | FORCE RLS（模板③）+ 应用禁用 owner 连接 + T6 固化；运维直连审计属 Stage 2 范畴 |
| golang-migrate 项目维护放缓 | 低 | SQL-first 纯文件格式，goose/atlas 均可读同目录，工具可替换性即缓解 |
| down 迁移在含数据表上造成静默数据丢失 | 中 | §2.1 不可逆标注 + down 抛异常；生产禁跑 down（部署文档写死，回退=前滚新迁移） |
| 顺序编号在并行分支冲突 | 低 | 1-2 人团队天然串行；T7 的 CI 检查兜底，冲突方 renumber |
| 会话变量方案在连接池下串租户（复用连接未重置） | 中 | 约定事务级 `SET LOCAL`（模板注释写明）；spec-1.1 中间件实现时以集成用例固化 |

## §7 DoD

- [ ] D1-D5 就位，T1-T8 全部通过（集成用例入 CI）
- [ ] `console migrate` 三子命令（up/down/version）可用且 `--help` 完整
- [ ] `make migrate-new` 生成物符合 §2.1（含模板注释）
- [ ] 0001 迁移在 compose dev-deps 的 PG 上手工验证通过
- [ ] RLS 模板四要素在 spec 文档与迁移注释中一致
- [ ] 系统表白名单机制有首条记录（tenants）
- [ ] 连接池串租户风险的 SET LOCAL 约定写入模板注释
- [ ] development-standards §5 与本 spec 无矛盾（复核）
- [ ] specs/README.md 与 roadmap 进度表更新
- [ ] commit 格式合规，独立 commit 序列

## §8 Q&A

**Q1 迁移工具：A. golang-migrate（★推荐） B. goose C. atlas**
推荐 A：SQL-first、生态最广、库形式可嵌 console 二进制（iofs）、咨询锁成熟；
B 能力相近但生态弱一档；C 声明式理念好但引入 HCL 心智且相对年轻，纯增学习成本。

**Q2 版本编号：A. 四位顺序号（★推荐） B. 时间戳**
推荐 A：可读、review 时顺序一目了然；时间戳解决的"并行分支冲突"在本团队规模
不存在，且 T7 有 CI 兜底。

**Q3 执行方式：A. 独立 `migrate` 子命令 + 部署期单点执行（★推荐） B. 服务启动自动迁移**
推荐 A：多副本启动竞争迁移是经典事故源；子命令形态天然适配 Helm pre-upgrade hook
（spec-0.10 衔接）与本地 make。B 仅省一条命令，风险不对称。

**Q4 down 策略：A. down 必写 + 不可逆显式标注（★推荐） B. up-only**
推荐 A：开发/测试期 down 是高频操作（T2 依赖）；生产不跑 down 由部署约定管，
而非取消能力。B 会让本地迭代退化为"删库重来"。

**Q5 RLS 强度：A. ENABLE + FORCE + 非 owner 应用角色（★推荐） B. 仅 ENABLE**
推荐 A：仅 ENABLE 时表 owner 默认绕过 policy——迁移与应用若共用角色即形同虚设；
FORCE + 角色分离让"绕过"必须是显式高权限操作，可审计。

## §9 实施计划

| 步骤 | 内容 | 估时 |
|---|---|---|
| 1 | D1 migrate 子命令 + D2 脚手架/CI 检查 + T1/T2/T3/T7/T8 | 0.75 天 |
| 2 | D4 首批迁移 + D3 模板定稿 | 0.5 天 |
| 3 | D5 RLS 集成断言 + T4/T5/T6 | 0.5 天 |
| 4 | DoD 收尾 | 0.25 天 |

总计 2 天。

## §10 后续 spec 关联

- spec-1.1：按 §2.2 模板建全部领域表 + 实现 `SET LOCAL app.tenant_id` 中间件；
- spec-1.8：threadstore 两表按模板建（rollout_events 的分区策略在该 spec 叠加）；
- spec-2.1：RLS 全面启用审计——盘点全部表对照白名单与模板合规性；
- spec-0.10：Helm pre-upgrade hook 调 `console migrate up`；
- spec-1.5：TimescaleDB 侧租户隔离策略参照本 spec 结论另行论证（超表 + RLS 兼容性）。

## §11 Changelog（frozen 后追加，不重写正文）

| 日期 | 变更 |
|---|---|
| 2026-08-14 | §10 遗留的"超表 + RLS 兼容性"已论证完毕：**互斥**——TimescaleDB 列存压缩不能用于挂 RLS 的表（`columnstore cannot be used on table with row security`，`deploy/scripts/probe-timescale-rls.sh` 实测）。设计文档 AD-10 已修订为"由数据库强制隔离"，新增等效形态（基表对应用角色零授权 + `security_barrier` 视图 + `WITH CHECK OPTION`）。**本 spec §2.2 模板不变**，仍是控制面租户表的默认且唯一形态；等效形态**仅限**指标 hypertable，由 spec-1.5 §2 逐表登记并被四项集成用例固化。§2.2 末"违反模板 = review 直接打回"据此加一条例外：等效形态表须在 spec 中显式登记，未登记者照旧打回 |
