-- 0006_agent_threads down（纯结构回滚，spec-0.6 §2.1）
DROP TABLE IF EXISTS agent_graph_edges;
DROP TABLE IF EXISTS agent_thread_queue;
DROP TABLE IF EXISTS agent_rollout_events;          -- 级联删除全部分区
DROP FUNCTION IF EXISTS agent_rollout_events_ensure_partition(date);
DROP TABLE IF EXISTS agent_threads;
ALTER TABLE agents DROP COLUMN IF EXISTS default_model;
