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
