# spec-0.7 配置框架

> **DRAFT — 待 user approve**（Stage 0 分级预批：push 即可开工，user 异议后修改）

## Header / 元数据

- **位置**：Stage 0 第 7 个功能点；前置 spec-0.1；被全部服务类 spec 消费；
  与 spec-0.10（k8s Secret→env 注入）、spec-2.7（平台侧 secret 管理）衔接；
- **安全权重**：全局 security.md「secrets 走环境变量/secret 管理 + 启动校验存在性 +
  永不入 git」与 CLAUDE.md 安全原则 5 的工程落地；
- **依赖审批**：本 spec 新增后端直接依赖（规则 8 事前审批制）——Go：`caarlos0/env`、
  `joho/godotenv`（dev-only）；Python：`pydantic-settings`。**approve 本 spec 即完成
  该三项依赖审批**，选型理由见 §8 Q1；
- **决策日期**：2026-08-09。

## §1 范围

### §1.1 包含（Deliverables）

| # | Deliverable | 文件清单 | 估算 | 说明 |
|---|---|---|---|---|
| D1 | Go 配置库 | `libs/config/` Go module（入 go.work）：struct tag 声明式加载、required 校验、`secret:"true"` 脱敏标记、聚合报错 | ~4 文件 ~250 LOC | 四个 Go 组件即时消费，共享库条件成立（§1.3） |
| D2 | Python 配置基类 | `skills/airush_skills/config.py`：pydantic-settings BaseSettings 子类约定（前缀、SecretStr、校验器） | 1 文件 ~60 LOC | skill 服务统一继承 |
| D3 | 命名与注入约定 | 本 spec §2.1 定版：`AIRUSH_<COMPONENT>_<KEY>`、env-only、每组件 `.env.example`（无真实值）入库 | 4 文件 ~80 行 | `.env` 本身在 .gitignore（spec-0.1 已覆盖） |
| D4 | 启动校验与安全 dump | 缺失/非法项**聚合列出后退出**（fail-fast 但一次报全）；`--print-config` 输出脱敏后配置（secret 显示 `***`） | 并入 D1/D2 | 排障能力与防泄漏并存 |
| D5 | 占位服务接入 | 四个 Go 二进制 + skills 包接入真实最小配置（`LOG_LEVEL`、`LISTEN_ADDR`），`--version`/启动路径走配置加载 | ~5 文件 ~100 LOC | 框架被真实使用而非空转 |

### §1.2 不包含

| 不做的事 | 理由 |
|---|---|
| 配置热更新（watch/reload） | §8 Q4 决策不做：12-factor 语义下配置变更 = 重启/滚动发布，热更新引入一致性复杂度无当前收益 |
| 配置中心（etcd/consul/nacos） | 组件数与配置量级用不上；env 注入在 k8s 下已是标准通道 |
| k8s Secret 对象的创建与管理 | spec-0.10（chart 内引用）与 spec-2.7（平台 secret 管理）专项 |
| 前端运行时配置 | Vite 构建期 `VITE_` 注入已够 Stage 0/1；运行时配置端点待真实需求（多环境同镜像）出现 |
| 配置项加密存储（SOPS 等） | 仓库不存真实值（.env.example 全占位），无加密对象 |
| 多环境 profile 文件体系 | env-only 决策（§8 Q2）下不存在 profile 文件 |

### §1.3 例外说明

D1 建 `libs/config` 共享库——spec-0.1"第二个使用者前不抽公共库"条款的**正向满足**
（四个组件即时消费），非例外；在此登记以保持条款执行透明。libs/ 目录由此激活，
后续入 libs/ 的库均需在对应 spec 中论证使用方 ≥2。

## §2 接口设计

### §2.1 命名与注入约定（定版）

- 变量名：`AIRUSH_<COMPONENT>_<KEY>`，如 `AIRUSH_CONSOLE_DB_URL`、`AIRUSH_GATEWAY_LISTEN_ADDR`；
  跨组件共用项用 `AIRUSH_COMMON_` 前缀（如 `AIRUSH_COMMON_LOG_LEVEL`，组件级可覆盖）；
- 注入通道：生产 = k8s env（来源 ConfigMap/Secret，spec-0.10）；本地 = shell env 或
  `.env`（godotenv 仅在非生产加载，加载顺序 env 优先于 .env）；
- `.env.example`：每组件根目录一份，列全部配置项 + 注释 + 占位值，与代码同 PR 更新
  （新增配置项不更新 example = review 打回）；
- secret 类字段：Go `secret:"true"` tag / Python `SecretStr`，二者语义等价。

### §2.2 Go API 形态（示意）

```go
type Config struct {
    ListenAddr string `env:"LISTEN_ADDR" default:":8080"`
    DBURL      string `env:"DB_URL" required:"true" secret:"true"`
    LogLevel   string `env:"LOG_LEVEL" default:"info" oneof:"debug,info,warn,error"`
}
cfg, err := config.Load[Config]("CONSOLE")   // 前缀拼装 + 校验聚合
```

## §3 行为契约

- 必填项缺失：启动退出码 2，stderr 一次性列出**全部**缺失/非法项（禁止挤牙膏式报错）；
- secret 值在任何输出通道（日志、panic、`--print-config`、错误消息）均不可见明文；
- `.env` 仅在 `AIRUSH_ENV != production` 时加载；生产镜像内不存在 .env 文件（0.10 保证）；
- 默认值在代码中唯一声明（tag），文档由 .env.example 承载——两处不一致视为缺陷。

## §4 测试用例

| # | 用例 | 目的 |
|---|---|---|
| T1 | 全量合法 env → 加载成功且值正确 | 主链路 |
| T2 | 缺 2 个必填 + 1 个非法枚举 → 一次列出 3 项后退出 2 | 聚合校验 |
| T3 | secret 字段在 --print-config 输出为 *** | 脱敏 |
| T4 | 构造 panic 场景 → 栈与错误输出无 secret 明文 | 泄漏防护 |
| T5 | env 与 .env 同名 → env 胜出 | 加载顺序 |
| T6 | AIRUSH_ENV=production 时 .env 被忽略 | 生产隔离 |
| T7 | Python BaseSettings 子类同语义（前缀/必填/SecretStr） | 双语言一致性 |
| T8 | .env.example 与 Config struct 字段一致性检查脚本 | 文档同步 |

## §5 与现有代码的 contract

- 新增：`libs/config`（go.work 登记）、Python config 基类、各组件 .env.example；
- 修改：四个占位 main.go（接入加载路径）、Makefile（T8 检查入 `make lint` 附属）；
- 不动：CI 结构、测试框架；
- 对后续 spec 的接口：§2.1 命名约定 + Load API 签名；新增配置项无需修订本 spec，
  新增**配置源类型**（如文件、远端）必须修订。

## §6 风险

| 风险 | 概率 | 缓解 |
|---|---|---|
| secret 经日志间接泄漏（第三方库 debug 日志打印连接串） | 中 | DB URL 等复合 secret 整体标记 secret；spec-0.9 日志层增全局 redaction 兜底（两道防线） |
| .env 被误提交含真实值 | 中 | .gitignore（已有）+ spec-0.3 gitleaks 扫描双保险 |
| 聚合校验实现遗漏字段类型（时间/大小等） | 低 | 类型解析失败归入非法项统一报告；新类型按需扩展 tag |
| caarlos0/env 维护风险 | 低 | 库极薄（反射 + tag），必要时 fork/内联成本 <1 天 |
| 组件间共用项（COMMON_）覆盖语义混淆 | 中 | 覆盖规则单元测试固化（组件级存在则胜出），.env.example 注释写明 |

## §7 DoD

- [ ] D1-D5 就位，T1-T8 通过
- [ ] 四个 Go 二进制启动路径均经 config.Load（无裸 os.Getenv，lint 规则或 review 核对）
- [ ] .env.example ×4 与实际字段一致（T8 入 CI）
- [ ] secret 脱敏在 Go/Python 双侧验证（T3/T4/T7）
- [ ] 生产不加载 .env 的行为有测试（T6）
- [ ] 新依赖三项在 go.mod/pyproject 中版本锁定
- [ ] README/开发文档增"本地配置"一节（.env 用法）
- [ ] development-standards 无矛盾复核
- [ ] specs/README.md 与 roadmap 进度表更新
- [ ] commit 格式合规，独立 commit 序列

## §8 Q&A

**Q1 Go 配置库：A. caarlos0/env + godotenv 薄组合（★推荐） B. viper C. 纯手写 os.Getenv**
推荐 A：与 env-only 决策严丝合缝，两库合计极薄、可 fork；B 为文件/watch/多格式
设计，在 env-only 下 90% 能力闲置还引入大依赖树（与依赖管控精神相悖）；
C 失去声明式校验/脱敏/聚合报错，每组件手写必然漂移。

**Q2 配置源：A. env-only（dev 辅以 .env）（★推荐） B. env + yaml 配置文件**
推荐 A：12-factor 正统，k8s 注入通道唯一，无"文件与 env 谁覆盖谁"的永恒困惑；
B 的场景（大体积结构化配置）当前不存在，出现时（如告警规则）应入库而非配置文件。

**Q3 命名：A. AIRUSH_ 统一前缀 + 组件段（★推荐） B. 各组件自定义前缀**
推荐 A：k8s 混排 env 时可读性与 grep 友好；COMMON_ 段解决共用项，覆盖语义显式。

**Q4 热更新：A. 不做（★推荐） B. fsnotify/信号 reload**
推荐 A：滚动发布即配置发布，与无状态设计（AD-1）天然一致；热更新的一致性问题
（部分副本新旧配置并存）在多副本下反而更难推理。

**Q5 secret 输出防护：A. 类型系统强制（tag/SecretStr）+ 测试固化（★推荐） B. 编码约定人肉遵守**
推荐 A：泄漏是 P0 级事故（安全原则 1），必须机制化；B 在 panic/第三方日志路径
必然失守。§6 另有日志层兜底形成双防线。

## §9 实施计划

| 步骤 | 内容 | 估时 |
|---|---|---|
| 1 | D1 libs/config + T1-T6 | 0.75 天 |
| 2 | D2 Python 基类 + T7 | 0.25 天 |
| 3 | D3/D5 约定落地与占位接入 + T8 | 0.25 天 |
| 4 | DoD 收尾 + 文档 | 0.25 天 |

总计 1.5 天。

## §10 后续 spec 关联

- spec-0.9：日志全局 redaction 兜底（本 spec §6 第一行的第二道防线）；
- spec-0.10：chart 中 ConfigMap/Secret → env 注入按 §2.1 通道实现；
- spec-1.x 全部服务：新配置项按约定添加并同步 .env.example；
- spec-2.7：平台侧 secret 管理（轮换、来源）在本框架之上叠加，不改加载 API。
