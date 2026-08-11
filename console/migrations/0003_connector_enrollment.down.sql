-- 0003_connector_enrollment down：列回收 + 状态域回三值（超集值降级为 offline）。
ALTER TABLE connectors
    DROP COLUMN IF EXISTS revoked_at,
    DROP COLUMN IF EXISTS enroll_token_expires_at,
    DROP COLUMN IF EXISTS enroll_token_hash;

UPDATE connectors SET status = 'offline'
    WHERE status NOT IN ('online', 'offline', 'degraded');
ALTER TABLE connectors ALTER COLUMN status SET DEFAULT 'offline';
ALTER TABLE connectors DROP CONSTRAINT connectors_status_check;
ALTER TABLE connectors ADD CONSTRAINT connectors_status_check
    CHECK (status IN ('online', 'offline', 'degraded'));
