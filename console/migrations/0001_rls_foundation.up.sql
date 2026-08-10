-- 0001_rls_foundation（spec-0.6 D4，user 评审 2026-08-10）
-- SYSTEM TABLE (no tenant scope): tenants —— 租户主档，白名单登记首条（spec-0.6 §2.2）。
--
-- 会话变量约定：应用在事务内 SET LOCAL app.tenant_id = '<uuid>'（连接池防串租户）；
-- 未设置时租户表 policy 判 NULL → 0 行（fail-closed）。
-- 租户表模板四要素（后续所有租户表必须照抄，见 spec-0.6 §2.2）：
--   ① tenant_id 复合主键前缀  ② ENABLE ROW LEVEL SECURITY
--   ③ FORCE ROW LEVEL SECURITY ④ tenant_isolation policy

-- 应用角色：仅承载权限集（NOLOGIN）；登录用户由部署侧创建并 GRANT airush_app，
-- 密码永不出现在迁移中。云托管环境角色可能预建，幂等包裹。
DO $$
BEGIN
    IF NOT EXISTS (SELECT FROM pg_roles WHERE rolname = 'airush_app') THEN
        CREATE ROLE airush_app NOLOGIN;
    END IF;
END
$$;

CREATE TABLE tenants (
    id         uuid        PRIMARY KEY DEFAULT gen_random_uuid(),
    name       text        NOT NULL,
    slug       text        NOT NULL UNIQUE CHECK (slug ~ '^[a-z0-9-]{2,32}$'),
    status     text        NOT NULL DEFAULT 'active' CHECK (status IN ('active', 'suspended')),
    created_at timestamptz NOT NULL DEFAULT now(),
    updated_at timestamptz NOT NULL DEFAULT now()
);

COMMENT ON TABLE tenants IS '租户主档（系统表，无租户语义，不挂 RLS；spec-0.6）';

GRANT USAGE ON SCHEMA public TO airush_app;
GRANT SELECT, INSERT, UPDATE, DELETE ON tenants TO airush_app;
