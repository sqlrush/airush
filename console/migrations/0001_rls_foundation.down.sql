-- 0001 回滚：仅结构回滚（spec-0.6 §2.1）。
-- airush_app 角色保留：可能已被环境级登录用户 GRANT 依赖，角色回收属运维操作不入迁移。
DROP TABLE IF EXISTS tenants;
