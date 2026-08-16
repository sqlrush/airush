-- 0005_llm_quota_usage（spec-1.7 D2，user approve 2026-08-15）
--
-- LLM 配额与用量在**平台侧**（spec-1.7 §8 Q1/Q2）：LiteLLM 是无状态纯路由，不持久化任何东西；
-- "谁、用了多少、还剩多少"记在这两张表——配额一份事实源，网关随时可换。
-- 两表都是租户表，逐表照抄 spec-0.6 §2.2 模板四要素（ENABLE/FORCE/policy/GRANT）。
--
-- 只记元数据，**永不记 prompt/响应内容**（AD-3；内容留存任何 Stage 都不做，spec-1.7 §1.2 #8）。

-- llm_quotas：租户月度 token 预算（§8 Q5：token 是所有供应商共同的计量单位；不按金额——不做计费）。
-- period 先只有 monthly；列存在是为了以后加 daily 时不改主键。
CREATE TABLE llm_quotas (
    tenant_id    uuid        NOT NULL REFERENCES tenants(id),
    period       text        NOT NULL DEFAULT 'monthly' CHECK (period IN ('monthly')),
    token_budget bigint      NOT NULL CHECK (token_budget >= 0),   -- 0 = 禁用 LLM
    hard_stop    boolean     NOT NULL DEFAULT true,               -- true 超额拒绝；false 只记指标
    updated_at   timestamptz NOT NULL DEFAULT now(),
    PRIMARY KEY (tenant_id, period)
);
ALTER TABLE llm_quotas ENABLE ROW LEVEL SECURITY;
ALTER TABLE llm_quotas FORCE ROW LEVEL SECURITY;
CREATE POLICY tenant_isolation ON llm_quotas
    USING (tenant_id = NULLIF(current_setting('app.tenant_id', true), '')::uuid);
GRANT SELECT, INSERT, UPDATE, DELETE ON llm_quotas TO airush_app;

-- llm_usage：一次 LLM 调用一行（§8 Q4：审计要能追到"这一次对话花了多少"，配额要精确到当月已用；
-- 日聚合丢 trace 关联）。Stage 1 不分区；保留策略随 spec-1.15 审计一并定。
CREATE TABLE llm_usage (
    tenant_id         uuid        NOT NULL REFERENCES tenants(id),
    id                uuid        NOT NULL DEFAULT gen_random_uuid(),
    at                timestamptz NOT NULL DEFAULT now(),
    model             text        NOT NULL,                 -- 平台逻辑名（chat-default），非供应商名
    upstream_model    text        NOT NULL DEFAULT '',      -- 网关实际命中的后端（fallback 后可能不同）
    agent_id          uuid,                                 -- spec-1.8 起填；Stage 1 允许 NULL
    session_id        text        NOT NULL DEFAULT '',
    trace_id          text        NOT NULL DEFAULT '',
    purpose           text        NOT NULL DEFAULT '',      -- 'chat' | 'inspection' | …（白名单在 1.8/1.9 收口）
    prompt_tokens     integer     NOT NULL CHECK (prompt_tokens >= 0),
    completion_tokens integer     NOT NULL CHECK (completion_tokens >= 0),
    total_tokens      integer     NOT NULL CHECK (total_tokens >= 0),
    cost_ref_micro    bigint,                               -- 网关回的参考成本（微美元），可空；不进计费
    stream            boolean     NOT NULL DEFAULT false,
    status            text        NOT NULL
                                  CHECK (status IN ('ok', 'upstream_error', 'quota_rejected', 'aborted')),
    -- 幂等键：Meter 生成 (trace_id, seq)，记账重试不双记（spec-1.7 §2.5）。
    idem_key          text        NOT NULL,
    PRIMARY KEY (tenant_id, id),
    UNIQUE (tenant_id, idem_key)
);
ALTER TABLE llm_usage ENABLE ROW LEVEL SECURITY;
ALTER TABLE llm_usage FORCE ROW LEVEL SECURITY;
CREATE POLICY tenant_isolation ON llm_usage
    USING (tenant_id = NULLIF(current_setting('app.tenant_id', true), '')::uuid);
-- 用量只增不改不删（审计语义）：应用角色无 UPDATE/DELETE。
GRANT SELECT, INSERT ON llm_usage TO airush_app;

CREATE INDEX llm_usage_tenant_at_idx ON llm_usage (tenant_id, at DESC);
-- 当月已用 = sum(total_tokens) WHERE at >= date_trunc('month', now())：走 (tenant_id, at) 即可，
-- 不建表达式索引——date_trunc 表达式索引在 RLS 谓词下常不被选用，实测再说。

-- seed：dev 租户默认月度预算（值与 AIRUSH_CONSOLE_LLM_DEFAULT_TOKEN_BUDGET 缺省一致：5 千万 token）。
-- 新租户的配额行由租户创建流程写（spec-2.1），Stage 1 只有 dev 租户。
INSERT INTO llm_quotas (tenant_id, period, token_budget, hard_stop)
VALUES ('00000000-0000-0000-0000-000000000001', 'monthly', 50000000, true)
ON CONFLICT (tenant_id, period) DO NOTHING;
