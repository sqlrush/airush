# spec-1.17 直连接入模式（AD-2② 平台直连数据库）

> **DRAFT — 待 user approve**（Stage 1 严格事前批准：approve 前不编码）

## Header / 元数据

- **位置**：Stage 1 接入组第 3 件（roadmap 序：1.2 → 1.17 → 1.3）；前置 spec-1.1
  （datasources/credentials 表、credcrypto 信封加密）、spec-1.2（指令分发器接口、gateway
  接入面）；被 spec-1.3/1.4（探针挂载）、spec-1.13（接入向导双模式 UI）消费；
- **上游决策**：AD-2②（平台与数据库同内网时直连采集/执行，免装 Connector）、AD-4 直连
  模式（凭据平台侧**加密保管**，明文禁入库/日志/prompt，已由 spec-1.1 credcrypto 落地）；
- **核心定位**：建立**通道无关的接入器抽象**——Connector（反向隧道）与 Direct（平台直连）
  两种实现共享同一"指令分发器/探针宿主"接口（spec-1.2 §10 已按此设计），使 spec-1.3 探针
  一次编写、两通道运行；本 spec 交付 Direct 通道 + 连接测试，探针本体归 spec-1.3；
- **依赖审批（规则 5 硬门槛 #4）**：新增 Go 直接依赖 **openGauss/PG 驱动**——复用 spec-0.6
  已批的 `jackc/pgx/v5`（openGauss 走 PG 协议族，MVP 蓝本），无新增第三方依赖；
- **安全权重**：直连凭据从 credcrypto 解密后**仅在建立 DB 连接的函数栈帧内存在**，禁日志/
  禁错误消息/禁 LLM prompt——本 spec 是 AD-4 直连模式的执行面，凭据边界第三道防线所在；
- **决策日期**：2026-08-11，待 user approve。

## §1 范围

### §1.1 包含（Deliverables）

| # | Deliverable | 文件清单 | 估算 | 说明 |
|---|---|---|---|---|
| D1 | 接入器抽象接口 | `connector/internal/accessor/`：`Accessor` 接口（Probe 采集入口占位 + Command 分发 + Close），Connector 实现（包装 spec-1.2 session）与 Direct 实现共享 | ~3 文件 ~200 LOC | 通道无关，§2.1 |
| D2 | Direct 接入器核心 | `console/internal/directconn/`：从 credcrypto 解密凭据 → pgx 连接池（每 datasource 一池，懒建/TTL 回收）→ 连接生命周期管理；engine_family=postgres 优先（openGauss 走 PG 协议） | ~4 文件 ~350 LOC | §2.2；§8 Q2 |
| D3 | 连接测试 API | `POST /api/v1/datasources/{id}/test-connection`：direct 模式解密连库跑 `SELECT 1` + 版本探测，结果不落库只回状态；connector 模式返回 `AR_COMMON_NOT_IMPLEMENTED`（隧道侧测试归 spec-1.2 后续） | ~2 文件 ~180 LOC | spec-1.1 §1.2 承接项 |
| D4 | 直连指令分发骨架 | Direct 接入器实现与 spec-1.2 相同的 PING/ECHO 分发器（通道换直连，验证接口对称）；采集/执行处理器留 spec-1.3/Stage 2 挂载点 | ~2 文件 ~150 LOC | §2.1 对称性验证 |
| D5 | 测试 | 单测（接口对称、连接池懒建/回收、凭据解密仅栈帧内）+ 集成（真 PG 容器：test-connection 成功/失败/超时、凭据错误、模式护栏、明文泄漏扫描） | ~5 文件 ~600 LOC | §4 |

### §1.2 不包含

| 不做的事 | 理由 |
|---|---|
| 采集探针本体（指标/慢日志/元数据采集逻辑） | spec-1.3/1.4 专项；本 spec 只提供 Direct 通道与探针宿主接口，探针插件后置 |
| 动作类执行（写操作/DDL） | AD-9 归 Stage 2 审批链；Direct 分发器 Stage 1 仅 PING/ECHO + 只读连接测试 |
| MySQL/达梦直连 | MVP 蓝本 openGauss（PG 协议族）；MySQL 族 Stage 3，达梦按 PG 抽象预留 engine_family 值 |
| 连接池跨副本共享 / 全局连接治理 | agent-runtime 多副本的连接治理属规模化（Stage 2）；Stage 1 单 console 副本每 datasource 独立池 |
| 直连模式的网络可达性探测/内网发现 | 运维前置（部署时保证同内网）；本 spec 假设网络可达，不可达表现为连接超时错误码 |
| 接入向导 UI（双模式选择界面） | spec-1.13 前端专项；本 spec 提供其后端 API（test-connection + 已有 datasources CRUD） |

### §1.3 例外说明

无偏离。Direct 接入器逻辑上"归接入组"（roadmap 命名 spec-1.17 表意），代码落 console
（直连是平台侧执行面，与控制面同进程最简）+ connector（共享接口定义），非新组件。

## §2 接口设计

### §2.1 通道无关接入器抽象（定版）

```
                    ┌─────────── Accessor 接口 ───────────┐
                    │ Dispatch(cmd) CommandResult          │
                    │ (Probe 采集入口——spec-1.3 填充)      │
                    │ Close()                              │
                    └──────────────┬──────────────────────┘
          ┌────────────────────────┴────────────────────────┐
   ConnectorAccessor（spec-1.2）                    DirectAccessor（本 spec）
   经 gateway session 下发指令              console 侧解密凭据 → pgx 直连库执行
   凭据在客户侧                             凭据平台加密保管（credcrypto 解密）
```

- 两实现对同一 `Accessor` 接口；agent-runtime / skill 执行面据 datasource.connect_mode 取实现，
  上层逻辑（探针、诊断）通道无关；
- spec-1.2 的 `session.Handler`（PING/ECHO 分发器）语义在此提升为 `Accessor.Dispatch`。

### §2.2 Direct 连接生命周期（定版）

1. 请求带 datasource_id → 查 datasources（direct 模式 + credential_id）；
2. credcrypto 解密 `secret_ciphertext` → username/password（**仅本栈帧**）；
3. pgx 连接池：`postgres://user:***@host:port/db`（懒建，每 datasource 一 `*pgxpool.Pool`，
   空闲 TTL 回收，池上限可配）；DSN 组装**禁日志**（含密码）；
4. 执行：`SELECT 1` / 版本探测 / 探针查询（spec-1.3）；只读；
5. Close：连接归池；datasource 删除/凭据轮换时销毁对应池。

### §2.3 新错误码（proto/errors.json 增量）

`AR_DATASOURCE_CONNECT_FAILED`(E5/502，直连建连失败：网络/认证/超时，details 携带类别不含凭据)、
`AR_DATASOURCE_TEST_TIMEOUT`(E5/504，连接测试超时)。

## §3 行为契约

- 直连凭据明文只在"解密→组装 DSN→建连"单函数栈帧内，日志/错误/prompt 三处零出现
  （credcrypto 之外的第三道防线，注入式验证）；
- Direct 通道 Stage 1 只读：Dispatch 仅接受 PING/ECHO 与只读探针类，动作类显式
  `AR_COMMON_NOT_IMPLEMENTED`（Stage 2 审批链接管前不开写路径）；
- 连接池按 datasource 隔离，跨租户不共享池（RLS 之外的连接层隔离）；
- test-connection 幂等只读、不落库、不改 datasource.health_status（健康态由 spec-1.3 采集驱动）；
- 兼容性：`Accessor` 接口是 1.3/Stage 2 的稳定挂载点，签名冻结后只增不改。

## §4 测试用例

| # | 用例 | 目的 |
|---|---|---|
| T1 | Direct 与 Connector 实现均满足 `Accessor` 接口（编译期断言 + 行为对称） | 通道无关成立 |
| T2 | 真 PG：test-connection 成功返回版本 | 主链路 |
| T3 | 凭据错误 → AR_DATASOURCE_CONNECT_FAILED，details 不含密码 | 认证失败 + 泄漏防线 |
| T4 | 不可达 host → 超时 AR_DATASOURCE_TEST_TIMEOUT | 超时路径 |
| T5 | connector 模式 datasource 调 test-connection → AR_COMMON_NOT_IMPLEMENTED | 模式护栏 |
| T6 | 全路径（含错误）日志/响应无凭据明文 | AD-4 第三道防线 |
| T7 | 连接池懒建 + 空闲 TTL 回收 + datasource 删除销毁池 | 生命周期 |
| T8 | Direct 分发器 PING/ECHO 与 spec-1.2 语义一致 | 接口对称 |
| T9 | 动作类指令 → AR_COMMON_NOT_IMPLEMENTED（只读护栏） | Stage 1 边界 |
| T10 | 每个新错误码 ≥1 触发 | 错误码纪律 |

## §5 与现有代码的 contract

- 修改：proto/errors.json（增量）、console httpapi（test-connection 路由）、
  console server 装配（directconn 注入）、connector 模块（Accessor 接口定义 + Connector 包装）、
  .env.example（连接池参数）、dev-verify（test-connection 断言）；
- 新增：connector/internal/accessor、console/internal/directconn；
- 不动：spec-1.1 datasources/credentials schema（本 spec 纯消费）、spec-1.2 gateway 接入面
  （Connector 通道不变，仅被 Accessor 包装）、credcrypto API；
- 对后续：Accessor 是 spec-1.3 探针宿主、Stage 2 执行器的挂载接口。

## §6 风险

| 风险 | 概率 | 缓解 |
|---|---|---|
| 直连凭据解密后泄漏（日志/错误/prompt） | 中/致命 | 单函数栈帧约束 + DSN 组装禁日志 + T3/T6 注入验证 + review 检查该路径无 dump 类调试代码 |
| 连接池泄漏（datasource 删除后池不回收）耗尽 DB 连接 | 中 | 空闲 TTL + datasource 删除钩子销毁池（spec-1.1 DeleteDatasource 增 hook）+ T7 |
| openGauss 与原生 PG 协议差异（版本探测/SELECT 1 之外） | 中 | Stage 1 只做 SELECT 1 + 版本；engine 字段记录具体引擎，深度差异随 spec-1.3 采集暴露再处理 |
| 直连模式误开写路径（越过审批） | 低/高危 | Dispatch 白名单 Stage 1 仅只读类型，动作类硬拒（T9）；写路径解锁是 Stage 2 审批链的显式动作 |
| 每 datasource 一池在多数据源时连接数膨胀 | 低 | 池上限可配 + 懒建（未用不建）+ 空闲回收；规模化治理 Stage 2 |

## §7 DoD

- [ ] D1-D5 就位，T1-T10 全过（集成入 CI）
- [ ] Accessor 接口 Connector/Direct 双实现编译期断言 + 行为对称（T1/T8）
- [ ] 凭据全路径无明文（T3/T6 证据附 PR）
- [ ] test-connection 对真 openGauss + 原生 PG 各验一次（PG 协议族抽象成立）
- [ ] 连接池生命周期（懒建/TTL/删除销毁）实证（T7）
- [ ] 只读护栏：动作类硬拒（T9）
- [ ] 新错误码注册 + 触发用例 + 生成物同步
- [ ] 覆盖率合并口径达标（console/connector 各 ≥80%）
- [ ] .env.example / dev-verify 同步
- [ ] specs 索引与 roadmap 进度表更新；CHANGELOG Unreleased 追加
- [ ] commit 格式合规，独立 PR

## §8 Q&A

**Q1 Direct 接入器代码归属：A. console 内（与控制面同进程）（★推荐） B. 独立 direct-connector 组件 C. connector 模块内**
推荐 A：直连是"平台与库同内网"的本地部署形态，凭据由 console 的 credcrypto 保管、
datasources schema 也在 console——同进程解密连库最简、无跨进程传密钥；B 徒增部署单元与
密钥传递面；C connector 是客户侧二进制语义，塞平台侧直连逻辑混淆职责。接口定义放
connector（共享），实现放 console。

**Q2 连接池粒度：A. 每 datasource 一池，懒建 + 空闲 TTL 回收（★推荐） B. 全局共享池 C. 每请求新建连接**
推荐 A：每 datasource 独立凭据/目标，天然按源隔离（连接层隔离补 RLS）；懒建避免未用数据源
占连接，TTL 回收控总量；B 需按源切换 DSN 失去池化意义且混淆隔离；C 无池化，高频探针下
建连开销与 DB 连接风暴。

**Q3 通道抽象位置：A. 提炼 Accessor 接口，Connector/Direct 双实现（★推荐） B. Direct 单独一套不与 Connector 抽象**
推荐 A：spec-1.2 §10 已按"指令分发器通道无关"设计,本 spec 兑现它——探针(spec-1.3)一次
编写两通道运行是接入组的核心价值；B 会让探针/诊断逻辑按通道分叉,维护翻倍。

**Q4 test-connection 结果处理：A. 只读不落库、不改 health_status（★推荐） B. 落 health_status**
推荐 A：test-connection 是接入向导的即时校验(一次性),与周期采集驱动的 health_status
(spec-1.3)语义不同;混用会让"手测一次"污染健康时间线。health_status 归采集。

## §9 实施计划

| 步骤 | 内容 | 估时（评审轮次口径） |
|---|---|---|
| 1 | D1 Accessor 接口 + Connector 包装 + T1/T8（先立抽象） | 1 轮 |
| 2 | D2 Direct 连接生命周期 + 连接池 + T7 | 1-2 轮 |
| 3 | D3 test-connection API + T2/T3/T4/T5 + 凭据泄漏防线 T6 | 1-2 轮 |
| 4 | D4 直连分发器 + 只读护栏 T9 + 新错误码 T10 | 1 轮 |
| 5 | 集成 + dev-verify + 覆盖率 + DoD 收尾 | 1 轮 |

## §10 后续 spec 关联

- spec-1.3/1.4：采集探针实现 `Accessor` 的 Probe 入口，两通道运行；
- spec-1.13：接入向导消费 test-connection + datasources CRUD 做双模式选择 UI；
- Stage 2 审批链：Direct/Connector 的 Dispatch 从只读解锁到动作类（AD-9 一次性令牌）；
- spec-1.5：采集数据经 Direct 通道上报路径与 Connector 通道归一到数据接入层。
