-- 0002_domain_model down：逆依赖序回滚全部领域表与本迁移的 seed。
DROP TABLE IF EXISTS idempotency_keys;
DROP TABLE IF EXISTS datasource_aliases;
DROP TABLE IF EXISTS datasources;
DROP TABLE IF EXISTS datasource_credentials;
DROP TABLE IF EXISTS agents;
DROP TABLE IF EXISTS datasource_groups;
DROP TABLE IF EXISTS connectors;
DROP TABLE IF EXISTS users;
DELETE FROM tenants WHERE id = '00000000-0000-0000-0000-000000000001';
