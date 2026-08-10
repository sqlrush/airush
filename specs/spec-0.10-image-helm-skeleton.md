# spec-0.10 镜像构建与 Helm chart 骨架

> **DRAFT — 待 user approve**（Stage 0 分级预批：push 即可开工，user 异议后修改）

## Header / 元数据

- **位置**：Stage 0 第 10 个功能点；前置 spec-0.3（image job 骨架）、0.6（migrate hook）、
  0.7（env 注入）、0.9（观测配置）；spec-0.12 验收的部署面；
- **配套规则**：CLAUDE.md 规则 8（所有组件经 Helm 部署，禁 kubectl 手改线上）；
  `docs/k8s-scaling-design.md` §1（伸缩矩阵）、§1.1（存储按环境形态）、§2.3（护栏）、
  §3（Stage 0 落地项：HPA 基线 + requests/limits + PDB 进 chart 模板）；
- **本 spec 定版内容**：k8s-scaling-design 悬置的 **requests/limits 基线数值**（§2.3）；
- **决策日期**：2026-08-09。

## §1 范围

### §1.1 包含（Deliverables）

| # | Deliverable | 文件清单 | 估算 | 说明 |
|---|---|---|---|---|
| D1 | Dockerfile 模板 | `deploy/docker/go.Dockerfile`（参数化 COMPONENT，builder→distroless/static，nonroot）、`python.Dockerfile`（uv→3.12-slim）、`frontend.Dockerfile`（node→nginx-unprivileged）+ `.dockerignore` + `make images` | 4 文件 ~160 行 | §8 Q1/Q5 |
| D2 | Helm umbrella chart | `deploy/charts/airush/`：Chart.yaml、分层 values、`_helpers.tpl`（统一 labels/selector）、四个 Go 服务 + skills + frontend 的 subchart（Deployment/Service/SA + securityContext 基线 + PDB + HPA 模板开关） | ~30 文件 ~700 行 | §8 Q2 |
| D3 | 资源基线数值 | values.yaml 默认值 = §2.3 两张表（k8s 工作负载 + 私有化存储 Pod） | 并入 D2 | 本 spec 定版，实测修正走修订 |
| D4 | kind 本地环境 | `deploy/kind/config.yaml` + `make dev-up/down`（kind 创建→构建镜像→kind load→helm install，`storage.builtin=true` 拉起最小 PG/Redis） | ~3 文件 ~150 行 | k8s-scaling-design §1.1 开发形态 |
| D5 | migrate hook | pre-upgrade/pre-install Job 模板：调 `console migrate up`（spec-0.6 Q3 衔接） | 1 文件 ~50 行 | 失败即发布中止 |

### §1.2 不包含

| 不做的事 | 理由 |
|---|---|
| CD/生产部署流水线 | 无生产环境；Stage 1 末与环境同立 spec |
| ingress/TLS/域名 | 本地 kind 用 port-forward 足够；对外暴露随部署 spec |
| Neo4j/TimescaleDB subchart | 消费方分别在 Stage 3 / spec-1.5，届时按 storage.builtin 框架增量添加 |
| 镜像签名与 SBOM（cosign/syft） | 供应链加固归 Stage 2 安全域（spec-2.9 关联），先建再补不冲突 |
| 多架构镜像（arm64） | 同 spec-0.3 决策；kind 在 OrbStack 下 amd64 兼容运行 |
| CloudNativePG 等 operator | 私有化专项 spec-4.5；开发态最小 statefulset 足够且启动更快 |

### §1.3 例外说明

storage.builtin 子 chart 使用**官方镜像 + 自写最小模板**而非 bitnami——bitnami 镜像
2025 起授权与免费 tag 政策生变，供应链不可控（§8 Q3），此为对 k8s-scaling-design
§1.1"Helm 内置存储"实现路径的收窄，不改其结论。

## §2 接口设计

### §2.1 镜像规范（定版）

- 命名：`ghcr.io/sqlrush/airush/<component>`；tag：`<git-sha>`（不可变）+ `latest`（main 浮动）；
- 全部 non-root（uid 65532）、`readOnlyRootFilesystem: true`、无 shell（distroless）；
  每镜像同 sha 提供 `-debug` 变体（busybox 层）仅手工排障用，禁入 values 默认；
- Go：`CGO_ENABLED=0` 静态编译，`-trimpath -ldflags "-s -w -X main.version=..."`；
- 镜像内无 .env、无任何配置文件——配置只经 env（spec-0.7 §3 契约的部署面）。

### §2.2 values 分层（定版）

```
values.yaml            # 组件开关、镜像、资源基线（§2.3）、storage.builtin=false
values-dev.yaml        # kind：storage.builtin=true、副本=1、观测端点指向 obs 栈
（生产 values 由部署环境仓库/私有化包持有，不入本仓）
```

### §2.3 资源基线（定版，k8s-scaling-design 引用槽位）

**k8s 工作负载（request / limit）**：

| 组件 | CPU | 内存 | 依据 |
|---|---|---|---|
| agent-runtime | 0.5 / 2 | 1Gi / 3Gi | IO 密集（等 LLM/skill），每副本 ~20 会话，内存大头为上下文组装 |
| 接入网关 | 0.5 / 2 | 512Mi / 1Gi | 网络密集，~2000 mTLS 长连接/副本 |
| 控制台 API | 0.25 / 1 | 256Mi / 1Gi | 轻量 REST |
| LLM 网关（LiteLLM） | 0.5 / 2 | 1Gi / 2Gi | Python 代理 + token 计量 |
| 记忆服务（Stage 3 启用槽位） | 1 / 2 | 2Gi / 4Gi | 摄入 + 三路混合检索 |
| embedding（Stage 3 启用槽位） | 4 / 8 + 1 GPU | 8Gi / 16Gi | BGE-M3 fp16 ~4GB 显存 |
| skill 常驻型 | 1 / 4 | 2Gi / 4Gi | 分析型 CPU 密集，limit 放宽供爆发 |
| skill Job 型 | 1 / 2 | 2Gi / 4Gi | 单次任务即释放 |
| 数据接入层 | 0.5 / 2 | 1Gi / 2Gi | 批量写，瓶颈在下游 |

**私有化 in-k8s 存储 Pod（生产 SaaS 不适用）**：

| 组件 | CPU | 内存 | 备注 |
|---|---|---|---|
| PostgreSQL | 4 / 8 | 16Gi / 24Gi | 控制面 + threadstore |
| TimescaleDB | 4 / 8 | 16Gi / 24Gi | 指标 + 分区压缩 |
| Neo4j | 4 / 8 | 16Gi / 32Gi | 内存敏感度最高，私有化最低硬件门槛由它决定 |
| Redis | 1 / 2 | 4Gi / 8Gi | maxmemory + LRU |
| MinIO | 1 / 2 | 4Gi / 8Gi | 备份/冷归档 |

策略：**requests 保底、limits 允许爆发（Burstable）**；kind dev values 整体除 4。
数值为 Stage 1 容量演练（k8s-scaling-design §2.3）前的工程估计，实测偏差 >30% 修订本表。

### §2.4 稳定性护栏模板（k8s-scaling-design §2.3 落地）

- 每 subchart：PDB（`maxUnavailable: 1`，副本 >1 时启用）+ HPA 模板（默认关，
  spec-1.16 起按信号矩阵逐组件开启）+ 探针（liveness/readiness 指向 /healthz）。

## §3 行为契约

- `make dev-up` 在仅装 docker+kind+helm+kubectl 的机器一键到达"hello-world 可访问 +
  内置 PG 就绪 + migrate hook 已跑"状态；`make dev-down` 无残留；
- `helm upgrade` 幂等；migrate hook 失败 → 发布中止且旧版本继续服务；
- 全部 workload 通过 `helm lint` 与 kube-score 基线（无 privileged、无 latest 依赖、
  资源必填）；
- 镜像构建可重现：同 git-sha 两次构建产物功能一致（时间戳类差异豁免）。

## §4 测试用例

| # | 用例 | 目的 |
|---|---|---|
| T1 | make images 产出全部镜像且体积记录（Go <30MB 目标） | 构建链 + 体积基线 |
| T2 | make dev-up → kubectl get pods 全 Ready → 端口转发访问 /healthz | 一键环境 |
| T3 | 再次 helm upgrade 无变更 → 零重启 | 幂等 |
| T4 | 注入失败迁移 → hook 失败 → 发布中止、旧 Pod 存活 | migrate 门闩 |
| T5 | 容器内以 nonroot 运行、rootfs 只读（kubectl exec 探测 debug 变体） | 安全基线 |
| T6 | helm lint + kube-score 通过 | 模板质量 |
| T7 | storage.builtin=false 时不渲染任何存储对象 | 环境形态开关 |
| T8 | CI image job 被 Dockerfile 出现自动激活并推 ghcr（spec-0.3 D5 闭环） | 流水线衔接 |

## §5 与现有代码的 contract

- 新增：deploy/docker、deploy/charts、deploy/kind、make 目标；
- 修改：spec-0.3 image job 从骨架转激活（预期内，0.3 §10 已登记）；
- 不动：应用代码（healthz 端点 spec-0.9 D5 已备）；
- 对后续 spec 的接口：§2.1 镜像规范、§2.2 values 分层、§2.3 数值表、subchart 结构——
  新组件按既有 subchart 模式复制扩展。

## §6 风险

| 风险 | 概率 | 缓解 |
|---|---|---|
| 资源基线数值脱离实测（纯工程估计） | 高 | §2.3 声明修订条件（偏差>30%）；Stage 1 容量演练强制回填 |
| distroless 无 shell 增加现场排障成本 | 中 | -debug 变体 + kubectl debug 临时容器双路径，README 记录 |
| kind 在 OrbStack 下的网络/存储兼容性 | 低 | 用户环境即 OrbStack，T2 直接在其上验收；异常记录 workaround |
| Helm 模板膨胀失控（每组件复制粘贴漂移） | 中 | 公共逻辑收敛 _helpers.tpl；subchart 间 diff 审查列入 review checklist |
| ghcr 拉取速度影响 dev-up（国内网络） | 中 | dev-up 全部 kind load 本地镜像，不经 registry；仅 CI 推送走 ghcr |
| pre-upgrade hook 与多副本并发（roll 时新旧并存跑迁移） | 低 | golang-migrate 咨询锁（0.6）+ hook 单 Job 串行语义双保险 |

## §7 DoD

- [ ] D1-D5 就位，T1-T8 全部通过（记录附 PR）
- [ ] §2.3 数值表 = values.yaml 默认值（脚本比对或 review 逐项核对）
- [ ] k8s-scaling-design 增一行引用："基线数值定版于 spec-0.10 §2.3"
- [ ] 镜像体积基线记录（Go/Python/前端各一）
- [ ] securityContext 三要素（nonroot/只读 rootfs/降权能力集）全 workload 生效
- [ ] -debug 变体存在且默认 values 不引用
- [ ] README"本地 k8s 环境"一节（dev-up 到访问的完整路径）
- [ ] `make dev-down` 后 docker/kind 无残留资源（验证记录）
- [ ] specs/README.md 与 roadmap 进度表更新
- [ ] commit 格式合规，独立 commit 序列

## §8 Q&A

**Q1 Go 基础镜像：A. distroless/static（★推荐） B. alpine C. scratch**
推荐 A：无 shell/包管理器攻击面最小且带 CA 证书与 nonroot 用户；B 的 musl 与调试
便利以攻击面换取，排障需求由 -debug 变体满足；C 缺 CA/tzdata 还得手工补。

**Q2 chart 组织：A. umbrella + 组件 subchart（★推荐） B. 单 chart 多 Deployment**
推荐 A：组件启停（enabled 开关）、独立 values 命名空间、与"组件=部署单元"
（spec-0.1 Q2）同构；B 在组件增多后 values 变泥球。

**Q3 内置存储：A. 官方镜像 + 自写最小模板（★推荐） B. bitnami subchart C. operator**
推荐 A：开发态只要"起得来、连得上"，~80 行模板可控；B 有授权/镜像政策变动
供应链风险（2025 已发生）；C 是私有化生产形态（spec-4.5），开发态过重。

**Q4 镜像 tag：A. git-sha 不可变 + latest 浮动（★推荐） B. 仅 latest**
推荐 A：回滚与追溯要求不可变 tag；latest 仅供 dev 便利。版本语义 tag 由 spec-0.11 叠加。

**Q5 Dockerfile 形态：A. 每语言参数化单文件（★推荐） B. 每组件独立**
推荐 A：四个 Go 组件构建过程同构，独立文件必然漂移（改一处忘三处）；
组件差异全部经 build-arg 表达，出现真实结构差异时再分裂。

## §9 实施计划

| 步骤 | 内容 | 估时 |
|---|---|---|
| 1 | D1 三类 Dockerfile + make images + T1/T5 | 0.5 天 |
| 2 | D2/D3 chart 骨架 + 资源基线 + T6/T7 | 1 天 |
| 3 | D4 kind 环境 + T2/T3 | 0.5 天 |
| 4 | D5 migrate hook + T4；T8 CI 闭环 + DoD 收尾 | 0.5 天 |

总计 2.5 天。

## §10 后续 spec 关联

- spec-0.11：release 时镜像加版本 tag、chart version 联动；
- spec-0.12：验收全程跑在本 spec 的 dev-up 环境上；
- spec-1.5/Stage 3：TimescaleDB/Neo4j 按 storage.builtin 框架增 subchart；
- spec-1.16：HPA 按伸缩矩阵开启 + 容量演练回填 §2.3；
- spec-4.5：私有化离线包以本 chart 为基础叠加 operator 与数据保护（k8s-scaling §1.2）。
