# k8s 动态扩缩容设计

> 日期：2026-08-09 · 状态：定稿 · 落地于 spec-0.10（基线）与各组件 spec
> 目标：按"使用智能体的人数（活跃会话）+ 接入数据库数量（采集与巡检负载）"两个业务
> 驱动力自动伸缩 agent 容器与 skill 容器。

## 1. 伸缩信号矩阵

| 组件 | 业务驱动力 | 伸缩器 | 信号 | 副本范围 |
|---|---|---|---|---|
| agent-runtime | 活跃会话数 | **KEDA** | Redis Stream 待处理 turn 队列深度 + `active_sessions` 自定义指标（每副本目标 20 会话） | 2 → 50 |
| skill 常驻型（每 skill 独立） | 调用频率 | HPA（+KEDA http 可选） | RPS / CPU（分析型 skill 是 CPU 密集） | 长尾 skill **0** → N（KEDA scale-to-zero），热门 skill 1 → 20 |
| skill Job 型 | 巡检任务量 | k8s Job 天然按需 | 队列积压由调度中心控制并发上限（防打爆下游） | — |
| 接入网关 | Connector 连接数（∝ 数据库数） | HPA | 活跃 mTLS 长连接数（每副本目标 2000） | 2 → 20 |
| 数据接入层 | 采集写入量（∝ 数据库数） | KEDA | 写入队列滞留 + 写入延迟 | 2 → 30 |
| LLM 网关 / 记忆服务 / 控制面 API | 请求量 | HPA | RPS + P99 延迟 | 2 → 10 |
| 存储类（PG/Neo4j/Redis） | — | **不自动伸缩** | 容量告警 → 人工/计划内垂直扩容 | 固定副本 HA |

节点层：Cluster Autoscaler（或云厂商 Karpenter 等价物）；skill 池放独立节点组
（污点隔离），防止分析负载挤压 agent-runtime。

### 1.1 存储部署形态（按环境分，2026-08-09 定）

存储组件**不一刀切进 k8s**，按环境分形态，应用只见连接串（spec-0.7 配置注入，
Helm 存储子 chart 以 `storage.builtin` 开关）：

| 环境 | 形态 |
|---|---|
| 生产 SaaS | **存储不进业务 k8s 集群**：PG/Redis 用云托管（RDS 等），对象存储托管；Neo4j 独立部署（无成熟国内托管） |
| 开发/测试 | 全套进 k8s（kind + Helm 内置存储，`make dev-up` 一键全栈） |
| 私有化交付（Stage 4） | Helm 离线包内置存储（CloudNativePG / Neo4j Helm / Redis operator）——客户内网无云托管，硬需求 |

生产不进 k8s 的理由：小团队把主备/备份/监控外包给云厂商是决定性减负；
计算与存储故障域分离（k8s 集群升级/重建不牵连数据）；数据库 IO 绕开 CSI 层损耗。

### 1.2 私有化 in-k8s 存储的数据保护（spec-4.5 必含，Helm 默认值而非可选项）

数据在 PV 不在容器——删 pod 不丢数据是 PV 机制的基本保证。在此之上分层设防：
StatefulSet 固定身份重绑 PVC；`pvc-protection` finalizer（使用中 PVC 删不掉）；
PV `reclaimPolicy: Retain`（PVC 误删后磁盘数据保留可重绑）；StatefulSet
`persistentVolumeClaimRetentionPolicy: Retain`；RBAC 收敛（删 PVC/PV/数据库 CR
仅平台管理员角色）；PG 双副本跨节点反亲和（本地盘 storage class 下节点丢失由备库
接管，单副本仅限 PoC 且明示风险）。最后防线：包内置定时备份（PG WAL 归档 +
Neo4j dump → 客户 NFS/MinIO），**交付验收含真实恢复演练**。

**跨物理机镜像路线（spec-4.5 必含）**：数据库一律走**应用层复制**——
PG 用 CloudNativePG 主备实例经 podAntiAffinity 落不同物理机、各用本地 PV、
WAL 流复制同步（秒级 failover，保数据也保服务）；MinIO 分布式 EC ≥4 节点；
Neo4j Community 无复制（dump+重放兜底），硬 RPO 需求升 Enterprise 或换 FalkorDB。
存储层复制（Longhorn/Ceph/客户 SAN）仅用于通用杂项卷或客户既有设施。
**反模式禁令：禁止分布式存储卷上再叠数据库复制**（4-6 份数据+双重写放大）；
storage class 分开命名（local-db / replicated-general）按卷类型对号入座。

## 2. 关键机制

### 2.1 为什么 agent-runtime 能横向伸缩

AD-1 的直接红利：会话状态全部外置（PG/Redis），任何 pod 可处理任何租户的任何 turn。
KEDA 扩容后新 pod 立即消费队列；缩容走优雅排水：

1. preStop：停止领取新 turn；
2. 处理中 turn 跑完（上限 5 分钟，超时的 turn 状态已在 rollout 中，由其他 pod 按
   "rollout 为 SSOT"原则恢复续跑）；
3. `terminationGracePeriodSeconds: 330`。

### 2.2 巡检波峰的削平

巡检是计划性负载（CronJob 触发、租户级错峰调度），不直接冲击伸缩器：
调度中心把巡检任务写入队列并限制全局并发 → KEDA 按队列深度平滑扩容 →
波峰变成"队列变长 + 副本渐增"，而非瞬时打爆。租户巡检时间窗在接入时散列分配。

### 2.3 稳定性护栏

- 全组件设 resource requests/limits（spec-0.10 定基线数值）与 PDB（`maxUnavailable: 1`）；
- HPA/KEDA 冷却窗口：扩 60s / 缩 300s，防抖动；
- 每租户并发配额在应用层（agent-runtime 会话调度器）先行限流——伸缩解决容量，
  配额解决公平性，二者不混用；
- 容量演练：Stage 1 验收含"100 实例模拟采集 + 20 并发会话"的伸缩实测基线。

### 2.4 任务负载与 Agent 划分的扩展模型（2026-08-09 user 评审定）

UI 评审引入「任务」板块（定时巡检/长任务/一次性，见 ui-design-brief §2）后，
就"任务增长如何扩展"定下三条结论，作为将来任务调度 spec 的输入：

**a) 执行模型：agent 是逻辑身份，不是算力单元。**
agent 的全部"人格"外置于存储（配置/绑定/指令文档在 PG、记忆在 Neo4j、会话在
threadstore），runtime pod 无差别——谁接到 turn/任务谁临时加载该 agent 身份执行，
完毕写回释放（§2.1 的引申）。因此 1 个 agent 的 N 个任务由 N 个 pod 并行"扮演"
执行；并发粒度：**同一会话内逐轮串行，会话/任务之间并行**。推论：算力扩展 =
扩 pod 池，与 agent 数量无关。

**b) 任务容量四层治理（弹性扩容只是第一层，且必须有上限）。**

| 层 | 机制 | 备注 |
|---|---|---|
| 1 弹性 | KEDA 按队列深度扩池（§1 矩阵），有 maxReplicas/节点池/预算上限 | 管日常波动 |
| 2 调度 | 优先级队列：交互对话 > 定时巡检 > 长任务 Job；定时任务带延迟 SLO 窗口（如 ±30min）+ 错峰散列（§2.2） | 定时任务耐延迟，是天然削峰材料 |
| 3 配额 | 每租户并发/日运行次数配额（§2.3，随套餐分级）；LLM 月度预算打满自动暂停定时任务并通知 | 瓶颈常在 token 成本与客户侧 Connector 采集限速，先于算力到顶 |
| 4 治理 | 连续 30 天无异常的任务建议降频；重叠巡检项创建时提示合并；队列延迟 SLO 破线触发容量评审（人决策扩池或收紧配额） | 管长期 |

**c) Agent 划分 = 治理边界，禁止自动均摊。**
记忆实体中心（storage-selection §1.1）使实例在 agent 间重新绑定**零记忆搬迁**——
可拆分性架构免费支持；但 agent 承载指令文档域约定与审批路由（AD-9），自动
负载均衡式挪库会使"支付链路只读/双人审批"类约定静默错位，属事故不属优化。
正确形态：**过载检测（绑定实例数、指令文档 token 膨胀、巡检窗口完成率、跨业务
域混杂）→ 系统按业务域/引擎族给拆分方案（指令文档按适用范围继承）→ 人一键
批准生效 + 审计留痕**。若某 agent"任务跑不动"，病根必在池容量/配额/Connector
限速（b 层处理），拆 agent 不解决性能问题。

## 3. 分阶段落地

| 阶段 | 内容 |
|---|---|
| Stage 0（spec-0.10） | HPA 基线 + requests/limits + PDB 规范进 Helm chart 模板 |
| Stage 1（spec-1.16 验收） | KEDA 引入（队列驱动 agent-runtime/数据接入层）+ 伸缩实测基线 |
| Stage 2 | 租户配额限流对接；skill scale-to-zero 开启 |
| Stage 4 | 大客户独立节点池 / 独立 runtime 池（AD-10 升级路径） |

## 4. 修订历史

| 日期 | 变更 |
|---|---|
| 2026-08-09 | 初版 |
| 2026-08-09 | 新增 §2.4 任务负载与 Agent 划分的扩展模型（UI 评审「任务」板块引出，user 定）：pod 扮演式执行模型、任务容量四层治理、Agent 划分=治理边界禁止自动均摊 |
