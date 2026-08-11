-- 0002_domain_model（spec-1.1 D1，user approve 2026-08-10）
-- 七张领域表 + idempotency_keys，全部为租户表，逐表照抄 spec-0.6 §2.2 模板四要素。
-- updated_at 由应用层（repo 更新语句）维护，不用触发器——写路径全部收口在 repo 基座。
-- 租户内外键一律复合形态 (tenant_id, x_id)：RLS 之外的第二道防跨租户悬挂引用。

-- users：最小 actor 占位（spec-1.1 §8 Q1）；认证凭据类列由 spec-2.2 增列，禁在此扩展。
CREATE TABLE users (
    tenant_id  uuid        NOT NULL REFERENCES tenants(id),
    id         uuid        NOT NULL DEFAULT gen_random_uuid(),
    name       text        NOT NULL,
    role       text        NOT NULL DEFAULT 'dba' CHECK (role IN ('admin', 'dba', 'viewer')),
    created_at timestamptz NOT NULL DEFAULT now(),
    updated_at timestamptz NOT NULL DEFAULT now(),
    PRIMARY KEY (tenant_id, id)
);
ALTER TABLE users ENABLE ROW LEVEL SECURITY;
ALTER TABLE users FORCE ROW LEVEL SECURITY;
CREATE POLICY tenant_isolation ON users
    USING (tenant_id = NULLIF(current_setting('app.tenant_id', true), '')::uuid);
GRANT SELECT, INSERT, UPDATE, DELETE ON users TO airush_app;

-- connectors：客户侧代理注册表（写路径归 spec-1.2 注册协议，本 spec 只读）。
CREATE TABLE connectors (
    tenant_id         uuid        NOT NULL REFERENCES tenants(id),
    id                uuid        NOT NULL DEFAULT gen_random_uuid(),
    name              text        NOT NULL,
    location          text        NOT NULL DEFAULT '',
    version           text        NOT NULL DEFAULT '',
    status            text        NOT NULL DEFAULT 'offline'
                                  CHECK (status IN ('online', 'offline', 'degraded')),
    last_heartbeat_at timestamptz,
    cert_fingerprint  text        NOT NULL DEFAULT '',
    created_at        timestamptz NOT NULL DEFAULT now(),
    updated_at        timestamptz NOT NULL DEFAULT now(),
    PRIMARY KEY (tenant_id, id),
    UNIQUE (tenant_id, name)
);
ALTER TABLE connectors ENABLE ROW LEVEL SECURITY;
ALTER TABLE connectors FORCE ROW LEVEL SECURITY;
CREATE POLICY tenant_isolation ON connectors
    USING (tenant_id = NULLIF(current_setting('app.tenant_id', true), '')::uuid);
GRANT SELECT, INSERT, UPDATE, DELETE ON connectors TO airush_app;
CREATE INDEX connectors_list_idx ON connectors (tenant_id, created_at, id);

-- datasource_groups：主备/集群编组。
CREATE TABLE datasource_groups (
    tenant_id  uuid        NOT NULL REFERENCES tenants(id),
    id         uuid        NOT NULL DEFAULT gen_random_uuid(),
    name       text        NOT NULL,
    kind       text        NOT NULL CHECK (kind IN ('primary_standby', 'cluster')),
    created_at timestamptz NOT NULL DEFAULT now(),
    updated_at timestamptz NOT NULL DEFAULT now(),
    PRIMARY KEY (tenant_id, id)
);
ALTER TABLE datasource_groups ENABLE ROW LEVEL SECURITY;
ALTER TABLE datasource_groups FORCE ROW LEVEL SECURITY;
CREATE POLICY tenant_isolation ON datasource_groups
    USING (tenant_id = NULLIF(current_setting('app.tenant_id', true), '')::uuid);
GRANT SELECT, INSERT, UPDATE, DELETE ON datasource_groups TO airush_app;
CREATE INDEX datasource_groups_list_idx ON datasource_groups (tenant_id, created_at, id);

-- agents：智能体注册（assistant=租户助理，domain=领域 agent）。
CREATE TABLE agents (
    tenant_id           uuid        NOT NULL REFERENCES tenants(id),
    id                  uuid        NOT NULL DEFAULT gen_random_uuid(),
    name                text        NOT NULL,
    kind                text        NOT NULL CHECK (kind IN ('assistant', 'domain')),
    status              text        NOT NULL DEFAULT 'running'
                                    CHECK (status IN ('running', 'paused')),
    instruction_doc     text        NOT NULL DEFAULT '',
    instruction_version int         NOT NULL DEFAULT 1,
    created_at          timestamptz NOT NULL DEFAULT now(),
    updated_at          timestamptz NOT NULL DEFAULT now(),
    PRIMARY KEY (tenant_id, id),
    UNIQUE (tenant_id, name)
);
ALTER TABLE agents ENABLE ROW LEVEL SECURITY;
ALTER TABLE agents FORCE ROW LEVEL SECURITY;
CREATE POLICY tenant_isolation ON agents
    USING (tenant_id = NULLIF(current_setting('app.tenant_id', true), '')::uuid);
GRANT SELECT, INSERT, UPDATE, DELETE ON agents TO airush_app;
CREATE INDEX agents_list_idx ON agents (tenant_id, created_at, id);

-- datasource_credentials：直连模式凭据（AD-4②）。secret_ciphertext 为信封加密产物
-- （nonce‖enc(DEK)‖nonce‖ct），key_id 标识 KEK 版本；明文永不落库、永不出现在本表之外。
CREATE TABLE datasource_credentials (
    tenant_id         uuid        NOT NULL REFERENCES tenants(id),
    id                uuid        NOT NULL DEFAULT gen_random_uuid(),
    username          text        NOT NULL,
    secret_ciphertext bytea       NOT NULL,
    key_id            text        NOT NULL,
    enc_version       smallint    NOT NULL DEFAULT 1,
    rotated_at        timestamptz,
    created_at        timestamptz NOT NULL DEFAULT now(),
    PRIMARY KEY (tenant_id, id)
);
ALTER TABLE datasource_credentials ENABLE ROW LEVEL SECURITY;
ALTER TABLE datasource_credentials FORCE ROW LEVEL SECURITY;
CREATE POLICY tenant_isolation ON datasource_credentials
    USING (tenant_id = NULLIF(current_setting('app.tenant_id', true), '')::uuid);
GRANT SELECT, INSERT, UPDATE, DELETE ON datasource_credentials TO airush_app;

-- datasources：数据源主档。表级 CHECK 三条：双模式形态互斥 ×2 + 组内配对。
CREATE TABLE datasources (
    tenant_id      uuid        NOT NULL REFERENCES tenants(id),
    id             uuid        NOT NULL DEFAULT gen_random_uuid(),
    name           text        NOT NULL,
    engine_family  text        NOT NULL CHECK (engine_family IN ('postgres', 'mysql', 'dm')),
    engine         text        NOT NULL DEFAULT '',
    engine_version text        NOT NULL DEFAULT '',
    connect_mode   text        NOT NULL CHECK (connect_mode IN ('connector', 'direct')),
    connector_id   uuid,
    credential_id  uuid,
    host           text        NOT NULL,
    port           int         NOT NULL CHECK (port BETWEEN 1 AND 65535),
    database_name  text        NOT NULL DEFAULT '',
    group_id       uuid,
    group_role     text        CHECK (group_role IN ('primary', 'standby', 'replica', 'node')),
    agent_id       uuid,
    health_status  text        NOT NULL DEFAULT 'unknown'
                               CHECK (health_status IN ('unknown', 'ok', 'warn', 'crit')),
    created_at     timestamptz NOT NULL DEFAULT now(),
    updated_at     timestamptz NOT NULL DEFAULT now(),
    PRIMARY KEY (tenant_id, id),
    UNIQUE (tenant_id, name),
    FOREIGN KEY (tenant_id, connector_id)  REFERENCES connectors(tenant_id, id),
    FOREIGN KEY (tenant_id, credential_id) REFERENCES datasource_credentials(tenant_id, id),
    FOREIGN KEY (tenant_id, group_id)      REFERENCES datasource_groups(tenant_id, id),
    FOREIGN KEY (tenant_id, agent_id)      REFERENCES agents(tenant_id, id),
    CONSTRAINT mode_connector_shape CHECK
        (connect_mode <> 'connector' OR (connector_id IS NOT NULL AND credential_id IS NULL)),
    CONSTRAINT mode_direct_shape CHECK
        (connect_mode <> 'direct' OR (credential_id IS NOT NULL AND connector_id IS NULL)),
    CONSTRAINT group_role_pairing CHECK ((group_id IS NULL) = (group_role IS NULL))
);
ALTER TABLE datasources ENABLE ROW LEVEL SECURITY;
ALTER TABLE datasources FORCE ROW LEVEL SECURITY;
CREATE POLICY tenant_isolation ON datasources
    USING (tenant_id = NULLIF(current_setting('app.tenant_id', true), '')::uuid);
GRANT SELECT, INSERT, UPDATE, DELETE ON datasources TO airush_app;
CREATE INDEX datasources_list_idx       ON datasources (tenant_id, created_at, id);
CREATE INDEX datasources_connector_idx  ON datasources (tenant_id, connector_id)  WHERE connector_id IS NOT NULL;
CREATE INDEX datasources_credential_idx ON datasources (tenant_id, credential_id) WHERE credential_id IS NOT NULL;
CREATE INDEX datasources_group_idx      ON datasources (tenant_id, group_id)      WHERE group_id IS NOT NULL;
CREATE INDEX datasources_agent_idx      ON datasources (tenant_id, agent_id)      WHERE agent_id IS NOT NULL;

-- datasource_aliases：助理路由别名；随数据源删除级联（元数据，不构成引用保护）。
CREATE TABLE datasource_aliases (
    tenant_id     uuid        NOT NULL REFERENCES tenants(id),
    id            uuid        NOT NULL DEFAULT gen_random_uuid(),
    datasource_id uuid        NOT NULL,
    alias         text        NOT NULL,
    source        text        NOT NULL DEFAULT 'manual' CHECK (source IN ('manual', 'conversation')),
    created_at    timestamptz NOT NULL DEFAULT now(),
    PRIMARY KEY (tenant_id, id),
    UNIQUE (tenant_id, alias),
    FOREIGN KEY (tenant_id, datasource_id)
        REFERENCES datasources(tenant_id, id) ON DELETE CASCADE
);
ALTER TABLE datasource_aliases ENABLE ROW LEVEL SECURITY;
ALTER TABLE datasource_aliases FORCE ROW LEVEL SECURITY;
CREATE POLICY tenant_isolation ON datasource_aliases
    USING (tenant_id = NULLIF(current_setting('app.tenant_id', true), '')::uuid);
GRANT SELECT, INSERT, UPDATE, DELETE ON datasource_aliases TO airush_app;
CREATE INDEX datasource_aliases_ds_idx ON datasource_aliases (tenant_id, datasource_id);

-- idempotency_keys：变更接口幂等支持；TTL 清理（24h）由 spec-1.15 定时任务承载。
CREATE TABLE idempotency_keys (
    tenant_id       uuid        NOT NULL REFERENCES tenants(id),
    key             text        NOT NULL,
    request_hash    text        NOT NULL,
    response_status int         NOT NULL,
    response_body   jsonb       NOT NULL,
    created_at      timestamptz NOT NULL DEFAULT now(),
    PRIMARY KEY (tenant_id, key)
);
ALTER TABLE idempotency_keys ENABLE ROW LEVEL SECURITY;
ALTER TABLE idempotency_keys FORCE ROW LEVEL SECURITY;
CREATE POLICY tenant_isolation ON idempotency_keys
    USING (tenant_id = NULLIF(current_setting('app.tenant_id', true), '')::uuid);
GRANT SELECT, INSERT, UPDATE, DELETE ON idempotency_keys TO airush_app;
CREATE INDEX idempotency_keys_ttl_idx ON idempotency_keys (created_at);

-- seed：dev 租户 + dev 管理员（spec-1.1 §8 Q5，固定 UUID 供本地/CI/kind 三环境断言）。
INSERT INTO tenants (id, name, slug)
VALUES ('00000000-0000-0000-0000-000000000001', 'Dev Tenant', 'dev')
ON CONFLICT (id) DO NOTHING;
INSERT INTO users (tenant_id, id, name, role)
VALUES ('00000000-0000-0000-0000-000000000001',
        '00000000-0000-0000-0000-000000000010', 'dev', 'admin')
ON CONFLICT (tenant_id, id) DO NOTHING;
