package metrics

// 快照目录（spec-1.4 §2.2）：编译期常量，与指标目录同一口径（§8 Q1）。
// 每条 SQL 只 SELECT 系统视图/目录，内嵌 LIMIT 上限，绝无业务表行数据（AD-3）。
//
// 慢查询源按候选链探测：原生 PG 用 pg_stat_statements，openGauss 用 dbe_perf；
// 全不可用即 CapabilityMissing 降级（成功路径，供上层提示开启）。

// snapshotQuery 是一个源里的一条命名查询（一个 kind 可能需要多条，如表/列/索引）。
type snapshotQuery struct {
	// Name 供解析器分辨这批行属于哪部分。
	Name string
	SQL  string
	// MaxRows 是防御性的第二道上限（SQL 内已有 LIMIT；此值防目录 SQL 写错时
	// 把整库行数拉进内存）。
	MaxRows int
}

// snapshotSource 是某个 kind 的一个采集源候选。
type snapshotSource struct {
	// Name 记入 Snapshot.Source，标识实际命中的源。
	Name string
	// ProbeSQL 是能力探测：返回 ≥1 行即视为可用；空串表示无需探测（总是可用）。
	ProbeSQL string
	Queries  []snapshotQuery
}

// 排除的系统 schema（表结构快照只看业务对象）。
const excludedSchemas = `'pg_catalog', 'information_schema', 'pg_toast', 'cstore', 'dbe_perf', 'snapshot'`

// 行数防御上限（SQL 内已有 LIMIT；这些值防目录 SQL 写错时把整库行数拉进内存）。
const (
	schemaColumnMaxRows = 50_000 // 500 张表 × 100 列量级
	schemaIndexMaxRows  = 20_000
	configMaxRows       = 5_000
)

// postgresSlowlogSources 是 PG 协议族慢查询源候选链（按序探测，取首个可用）。
var postgresSlowlogSources = []snapshotSource{
	{
		// 原生 PG / PG 兼容发行版。最低兼容 PG 13（total_exec_time 在 PG 13 由
		// total_time 改名；更早版本走候选链下一个或降级 CapabilityMissing）。
		Name:     "pg_stat_statements",
		ProbeSQL: `SELECT 1 FROM pg_extension WHERE extname = 'pg_stat_statements'`,
		Queries: []snapshotQuery{{
			Name:    "slow_queries",
			MaxRows: SlowlogTopN,
			// query 列是**规范化**语句（字面量已为 $N 占位）——AD-3 关键前提。
			SQL: `SELECT s.queryid::text AS query_id, s.query AS text, s.calls AS calls,
					s.total_exec_time AS total_ms, s.mean_exec_time AS mean_ms,
					s.max_exec_time AS max_ms, s.rows AS rows, d.datname AS database
				FROM pg_stat_statements s
				LEFT JOIN pg_database d ON d.oid = s.dbid
				WHERE s.calls > 0
				ORDER BY s.total_exec_time DESC
				LIMIT 50`,
		}},
	},
	{
		// openGauss。dbe_perf 视图需 monadmin 权限；耗时单位为微秒故换算为毫秒。
		// 列名以 openGauss 3.x 文档为准，**尚未对真机双验**——spec-1.16 验收环境校验；
		// 校验失败时该源整体报错，链路降级为 CapabilityMissing 而非中断采集。
		Name:     "dbe_perf",
		ProbeSQL: `SELECT 1 FROM pg_namespace WHERE nspname = 'dbe_perf'`,
		Queries: []snapshotQuery{{
			Name:    "slow_queries",
			MaxRows: SlowlogTopN,
			SQL: `SELECT unique_sql_id::text AS query_id, query AS text, n_calls AS calls,
					total_elapse_time / 1000.0 AS total_ms,
					avg_elapse_time / 1000.0 AS mean_ms,
					max_elapse_time / 1000.0 AS max_ms,
					n_returned_rows AS rows, db_name AS database
				FROM dbe_perf.summary_statement
				WHERE n_calls > 0
				ORDER BY total_elapse_time DESC
				LIMIT 50`,
		}},
	},
}

// postgresSchemaSources 是表结构快照源（pg_catalog，无需能力探测）。
// 三条查询共用同一 "按总大小降序取前 N 张表" 的 CTE，故列/索引天然受同一上限约束。
var postgresSchemaSources = []snapshotSource{{
	Name: "pg_catalog",
	Queries: []snapshotQuery{
		{
			Name:    "tables",
			MaxRows: SchemaMaxTables,
			SQL: `WITH t AS (
					SELECT c.oid, n.nspname, c.relname,
						pg_total_relation_size(c.oid) AS size_bytes,
						c.reltuples AS row_estimate
					FROM pg_class c
					JOIN pg_namespace n ON n.oid = c.relnamespace
					WHERE c.relkind IN ('r', 'p')
						AND n.nspname NOT IN (` + excludedSchemas + `)
						AND n.nspname NOT LIKE 'pg\_toast%'
					ORDER BY pg_total_relation_size(c.oid) DESC
					LIMIT 500
				)
				SELECT nspname AS schema, relname AS name,
					size_bytes::text AS size_bytes, row_estimate::bigint::text AS row_estimate
				FROM t ORDER BY size_bytes DESC`,
		},
		{
			Name:    "columns",
			MaxRows: schemaColumnMaxRows,
			SQL: `WITH t AS (
					SELECT c.oid, n.nspname, c.relname
					FROM pg_class c
					JOIN pg_namespace n ON n.oid = c.relnamespace
					WHERE c.relkind IN ('r', 'p')
						AND n.nspname NOT IN (` + excludedSchemas + `)
						AND n.nspname NOT LIKE 'pg\_toast%'
					ORDER BY pg_total_relation_size(c.oid) DESC
					LIMIT 500
				)
				SELECT t.nspname AS schema, t.relname AS name, a.attname AS column_name,
					format_type(a.atttypid, a.atttypmod) AS data_type,
					(NOT a.attnotnull)::text AS nullable
				FROM t JOIN pg_attribute a ON a.attrelid = t.oid
				WHERE a.attnum > 0 AND NOT a.attisdropped
				ORDER BY t.nspname, t.relname, a.attnum`,
		},
		{
			Name:    "indexes",
			MaxRows: schemaIndexMaxRows,
			SQL: `WITH t AS (
					SELECT c.oid, n.nspname, c.relname
					FROM pg_class c
					JOIN pg_namespace n ON n.oid = c.relnamespace
					WHERE c.relkind IN ('r', 'p')
						AND n.nspname NOT IN (` + excludedSchemas + `)
						AND n.nspname NOT LIKE 'pg\_toast%'
					ORDER BY pg_total_relation_size(c.oid) DESC
					LIMIT 500
				)
				SELECT t.nspname AS schema, t.relname AS name, ic.relname AS index_name,
					i.indisunique::text AS is_unique,
					pg_get_indexdef(i.indexrelid) AS index_def
				FROM t
				JOIN pg_index i ON i.indrelid = t.oid
				JOIN pg_class ic ON ic.oid = i.indexrelid
				ORDER BY t.nspname, t.relname, ic.relname`,
		},
	},
}}

// postgresConfigSources 是实例配置快照源（pg_settings 全量，§8 Q6）。
// pg_settings 不含凭据类值；若某引擎出现敏感项，以排除表处理（受控口径追加）。
var postgresConfigSources = []snapshotSource{{
	Name: "pg_settings",
	Queries: []snapshotQuery{{
		Name:    "configs",
		MaxRows: configMaxRows,
		SQL: `SELECT name, setting AS value, COALESCE(unit, '') AS unit,
				COALESCE(source, '') AS source
			FROM pg_settings ORDER BY name`,
	}},
}}

// snapshotSourcesFor 返回某引擎族某 kind 的源候选链；未知组合返回 nil。
func snapshotSourcesFor(engineFamily, kind string) []snapshotSource {
	if engineFamily != "postgres" {
		return nil
	}
	switch kind {
	case SnapshotKindSlowlog:
		return postgresSlowlogSources
	case SnapshotKindSchema:
		return postgresSchemaSources
	case SnapshotKindConfig:
		return postgresConfigSources
	default:
		return nil
	}
}
