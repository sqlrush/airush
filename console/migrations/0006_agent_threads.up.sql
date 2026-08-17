-- 0006_agent_threads（spec-1.8 D2，user approve 2026-08-16）
--
-- agent-runtime 的线程模型外置到控制面 PG（AD-1：runtime 无状态，rollout 是 SSOT）：
--   agent_threads         线程元数据（状态机 idle/running/interrupted/archived/deleted）
--   agent_rollout_events  线程内事件流（event sourcing，append-only，按月 RANGE 分区）
--   agent_thread_queue    外置输入队列（steer / 排队消息；接纳关系与事件同事务）
--   agent_graph_edges     子 agent 拓扑（codexgo agentgraph.AgentGraphStore 的 PG 后端）
-- 四表都是租户表，逐表照抄 spec-0.6 §2.2 模板四要素（ENABLE/FORCE/policy/GRANT）。
-- 分区父表的 policy 对经父表的访问生效；应用只经父表读写（airush_app 对分区子表无授权）。
--
-- 另：agents 表新增 default_model（spec-1.8 §3 第 9 条，spec-1.1 已 shipped 表的兼容性追加，可空）。

ALTER TABLE agents ADD COLUMN IF NOT EXISTS default_model text;

-- ============================================================
-- agent_threads
-- ============================================================
CREATE TABLE agent_threads (
    tenant_id        uuid        NOT NULL REFERENCES tenants(id),
    id               uuid        NOT NULL,                     -- UUIDv7（runtime 生成）
    agent_id         uuid,                                     -- agents(id)；助理 agent 为系统内置行
    parent_thread_id uuid,                                     -- 子 agent / fork 来源
    fork_source_seq  bigint,                                   -- fork 自源线程的事件序号（0.147 prepare_fork 语义）
    title            text        NOT NULL DEFAULT '',
    status           text        NOT NULL DEFAULT 'idle'
                                 CHECK (status IN ('idle', 'running', 'interrupted', 'archived', 'deleted')),
    model            text        NOT NULL,                     -- 逻辑模型名（spec-1.7）
    running_pod      text,                                     -- 排水/恢复用：谁在跑（可重建，不是真相源）
    heartbeat_at     timestamptz,
    last_seq         bigint      NOT NULL DEFAULT 0,
    -- codexgo StoredThread 元数据（threadstore 契约需要的可变字段），一并存在线程行上
    metadata         jsonb       NOT NULL DEFAULT '{}'::jsonb,
    created_at       timestamptz NOT NULL DEFAULT now(),
    updated_at       timestamptz NOT NULL DEFAULT now(),
    archived_at      timestamptz,
    PRIMARY KEY (tenant_id, id),
    FOREIGN KEY (tenant_id, agent_id) REFERENCES agents(tenant_id, id),
    FOREIGN KEY (tenant_id, parent_thread_id) REFERENCES agent_threads(tenant_id, id)
);
ALTER TABLE agent_threads ENABLE ROW LEVEL SECURITY;
ALTER TABLE agent_threads FORCE ROW LEVEL SECURITY;
CREATE POLICY tenant_isolation ON agent_threads
    USING (tenant_id = NULLIF(current_setting('app.tenant_id', true), '')::uuid);
GRANT SELECT, INSERT, UPDATE, DELETE ON agent_threads TO airush_app;
CREATE INDEX agent_threads_tenant_status_heartbeat_idx ON agent_threads (tenant_id, status, heartbeat_at);
CREATE INDEX agent_threads_tenant_updated_idx ON agent_threads (tenant_id, updated_at DESC, id DESC);
CREATE INDEX agent_threads_tenant_parent_idx ON agent_threads (tenant_id, parent_thread_id);

-- ============================================================
-- agent_rollout_events（按月 RANGE 分区）
-- ============================================================
CREATE TABLE agent_rollout_events (
    tenant_id   uuid        NOT NULL,
    thread_id   uuid        NOT NULL,
    seq         bigint      NOT NULL,                          -- 线程内单调
    turn_id     uuid,
    event_type  text        NOT NULL,                          -- 白名单：见 spec-1.8 §3.3
    payload     jsonb       NOT NULL,                          -- ≤ 32KB 内联；超出截断并写 payload_ref
    payload_ref text,                                          -- 数据指针（Stage 4：对象存储）
    created_at  timestamptz NOT NULL DEFAULT now(),
    PRIMARY KEY (tenant_id, thread_id, seq, created_at)
) PARTITION BY RANGE (created_at);
ALTER TABLE agent_rollout_events ENABLE ROW LEVEL SECURITY;
ALTER TABLE agent_rollout_events FORCE ROW LEVEL SECURITY;
CREATE POLICY tenant_isolation ON agent_rollout_events
    USING (tenant_id = NULLIF(current_setting('app.tenant_id', true), '')::uuid);
-- 事件只增不改不删（审计语义）：应用角色无 UPDATE/DELETE；删线程 = 线程标 deleted，事件按保留策略清理。
GRANT SELECT, INSERT ON agent_rollout_events TO airush_app;
CREATE INDEX agent_rollout_events_tenant_thread_seq_idx ON agent_rollout_events (tenant_id, thread_id, seq);

-- 分区管理：按月建分区的幂等函数（spec-0.6 框架挂钩；runtime 启动期与月切换前调用），
-- 外加 DEFAULT 分区兜底——分区缺失时写入不丢，只是落到 default 分区（可事后 ATTACH 迁移）。
CREATE OR REPLACE FUNCTION agent_rollout_events_ensure_partition(month_start date)
RETURNS text
LANGUAGE plpgsql
AS $$
DECLARE
    part_name text := format('agent_rollout_events_%s', to_char(month_start, 'YYYYMM'));
    range_start timestamptz := date_trunc('month', month_start)::timestamptz;
    range_end   timestamptz := (date_trunc('month', month_start) + interval '1 month')::timestamptz;
BEGIN
    IF to_regclass(part_name) IS NULL THEN
        EXECUTE format(
            'CREATE TABLE %I PARTITION OF agent_rollout_events FOR VALUES FROM (%L) TO (%L)',
            part_name, range_start, range_end);
    END IF;
    RETURN part_name;
END;
$$;
CREATE TABLE agent_rollout_events_default PARTITION OF agent_rollout_events DEFAULT;
-- 当月与后续两个月预建（runtime 启动会继续滚动）
SELECT agent_rollout_events_ensure_partition(date_trunc('month', now())::date);
SELECT agent_rollout_events_ensure_partition((date_trunc('month', now()) + interval '1 month')::date);
SELECT agent_rollout_events_ensure_partition((date_trunc('month', now()) + interval '2 month')::date);

-- ============================================================
-- agent_thread_queue
-- ============================================================
CREATE TABLE agent_thread_queue (
    tenant_id        uuid        NOT NULL,
    thread_id        uuid        NOT NULL,
    id               uuid        NOT NULL,
    kind             text        NOT NULL CHECK (kind IN ('steer', 'queued')),
    payload          jsonb       NOT NULL,
    admitted_turn_id uuid,                                     -- 接纳该输入的 turn（NULL = 待接纳）
    created_at       timestamptz NOT NULL DEFAULT now(),
    PRIMARY KEY (tenant_id, id),
    FOREIGN KEY (tenant_id, thread_id) REFERENCES agent_threads(tenant_id, id) ON DELETE CASCADE
);
ALTER TABLE agent_thread_queue ENABLE ROW LEVEL SECURITY;
ALTER TABLE agent_thread_queue FORCE ROW LEVEL SECURITY;
CREATE POLICY tenant_isolation ON agent_thread_queue
    USING (tenant_id = NULLIF(current_setting('app.tenant_id', true), '')::uuid);
GRANT SELECT, INSERT, UPDATE, DELETE ON agent_thread_queue TO airush_app;
CREATE INDEX agent_thread_queue_tenant_thread_created_idx ON agent_thread_queue (tenant_id, thread_id, created_at);

-- ============================================================
-- agent_graph_edges
-- ============================================================
CREATE TABLE agent_graph_edges (
    tenant_id        uuid        NOT NULL,
    parent_thread_id uuid        NOT NULL,
    child_thread_id  uuid        NOT NULL,
    role             text        NOT NULL DEFAULT '',
    status           text        NOT NULL DEFAULT 'open' CHECK (status IN ('open', 'closed')),
    created_at       timestamptz NOT NULL DEFAULT now(),
    PRIMARY KEY (tenant_id, parent_thread_id, child_thread_id),
    UNIQUE (tenant_id, child_thread_id)                        -- 一个子线程只有一个父（upsert 换父）
);
ALTER TABLE agent_graph_edges ENABLE ROW LEVEL SECURITY;
ALTER TABLE agent_graph_edges FORCE ROW LEVEL SECURITY;
CREATE POLICY tenant_isolation ON agent_graph_edges
    USING (tenant_id = NULLIF(current_setting('app.tenant_id', true), '')::uuid);
GRANT SELECT, INSERT, UPDATE, DELETE ON agent_graph_edges TO airush_app;
