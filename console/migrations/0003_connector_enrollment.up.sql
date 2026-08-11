-- 0003_connector_enrollment（spec-1.2 D2，user 评审 2026-08-11）
-- connectors 表增量：状态机六值化（§2.4）+ 注册令牌哈希与吊销时间戳。
-- enroll token 明文永不落库（仅 SHA-256）；注册成功后哈希置 NULL 最小化泄漏面。

ALTER TABLE connectors DROP CONSTRAINT connectors_status_check;
ALTER TABLE connectors ADD CONSTRAINT connectors_status_check
    CHECK (status IN ('pending', 'enrolled', 'online', 'degraded', 'offline', 'revoked'));
ALTER TABLE connectors ALTER COLUMN status SET DEFAULT 'pending';

ALTER TABLE connectors
    ADD COLUMN enroll_token_hash       text,
    ADD COLUMN enroll_token_expires_at timestamptz,
    ADD COLUMN revoked_at              timestamptz;

COMMENT ON COLUMN connectors.enroll_token_hash IS
    '一次性注册令牌 SHA-256（hex）；enroll 成功或吊销后置 NULL（spec-1.2 §2.3）';
