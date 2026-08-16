-- 0005_llm_quota_usage down（纯结构回滚，spec-0.6 §2.1）
DROP TABLE IF EXISTS llm_usage;
DROP TABLE IF EXISTS llm_quotas;
