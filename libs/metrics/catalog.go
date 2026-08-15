package metrics

// CatalogEntry 是目录中一条指标的定义（受控口径，spec-1.3 §8 Q1：编译期常量）。
type CatalogEntry struct {
	// Name 遵循两层命名（spec-1.5 §2.6）：db.* = 跨引擎规范指标，pg.*/mysql.*/dm.* = 引擎特有。
	Name string
	Unit string
	// SQL 返回单行单列，列别名 "value"（数值）。只读聚合系统视图，无行级数据（AD-3）。
	SQL string
	// AltSQL 是同引擎族内的方言回退：主 SQL **执行报错**（如函数不存在）时改用它。
	// openGauss 承 PG 9.2 血统，WAL 位置函数仍是 pg_last_xlog_* 命名，与 PG 10+ 的
	// pg_last_wal_* 不同族名。空串表示无回退。
	AltSQL string
	// Nullable 标注该指标在某些角色/版本可能无值（如复制延迟在主库为空）——
	// 无值时该指标缺采（partial），不视为错误。
	Nullable bool
	// EntityKind 声明本条 series 是否带实体维度、实体是什么（spec-1.5 §2.6）。
	// 空串 = 无实体（entity_id 恒为空）。**非空时必须在此声明**——未声明的实体
	// 一律拒绝入库，这是 AD-3 从"label 键白名单"升级来的防线：泛化后 entity_id
	// 是个能塞任意文本的口子，只有目录声明能把它关上。
	EntityKind string
}

// PostgresCatalog 是 openGauss/PG 协议族的 Stage-1 指标目录。
// 每条 SQL 仅 SELECT 聚合系统视图（pg_stat_*/pg_settings 等），绝无业务表行数据。
//
// 命名分两层（spec-1.5 §2.6，2026-08-14 由单层 pg.* 改造）：
//   - db.*  规范指标——跨引擎语义一致、单位固定。skill 优先用这层，Stage 3 接
//     MySQL/达梦时由各自目录实现同名条目，skill 不必写第二遍；
//   - pg.*  PG 族特有——别的引擎没有对等概念，不硬凑。"没有的就是没有"是正常路径，
//     与 CapabilityMissing 同一语义。
var PostgresCatalog = []CatalogEntry{
	{
		Name: "db.connections.active", Unit: UnitCount,
		SQL: `SELECT count(*)::float8 AS value FROM pg_stat_activity WHERE state = 'active'`,
	},
	{
		Name: "db.connections.total", Unit: UnitCount,
		SQL: `SELECT count(*)::float8 AS value FROM pg_stat_activity`,
	},
	{
		Name: "db.connections.max", Unit: UnitCount,
		SQL: `SELECT setting::float8 AS value FROM pg_settings WHERE name = 'max_connections'`,
	},
	{
		Name: "db.transactions.commit_total", Unit: UnitCount,
		SQL: `SELECT COALESCE(sum(xact_commit),0)::float8 AS value FROM pg_stat_database`,
	},
	{
		Name: "db.transactions.rollback_total", Unit: UnitCount,
		SQL: `SELECT COALESCE(sum(xact_rollback),0)::float8 AS value FROM pg_stat_database`,
	},
	{
		Name: "db.cache.hit_ratio", Unit: UnitRatio,
		SQL: `SELECT COALESCE(sum(blks_hit)::float8 / NULLIF(sum(blks_hit + blks_read), 0), 0)
			AS value FROM pg_stat_database`,
	},
	{
		Name: "db.locks.waiting", Unit: UnitCount,
		SQL: `SELECT count(*)::float8 AS value FROM pg_locks WHERE NOT granted`,
	},
	{
		Name: "db.transactions.longest_seconds", Unit: UnitSeconds,
		SQL: `SELECT COALESCE(EXTRACT(EPOCH FROM max(now() - xact_start)), 0)::float8
			AS value FROM pg_stat_activity WHERE xact_start IS NOT NULL`,
	},
	{
		// 原名 pg.database.size_bytes。改规范名时一并把 "database" 换成 "storage"：
		// 这条统计的是**实例上全部库的总字节数**，而 database 一词在 MySQL 里指
		// schema、在 openGauss 里指库，跨引擎歧义大。
		Name: "db.storage.size_bytes", Unit: UnitBytes,
		SQL: `SELECT COALESCE(sum(pg_database_size(datname)), 0)::float8
			AS value FROM pg_database WHERE datistemplate = false`,
	},
	{
		// 规范形态的复制延迟：秒。MySQL 的 Seconds_Behind_Master 天然是秒，
		// PG 族这边用"最后重放事务的时间戳距今多久"换算，两边语义对齐。
		Name: "db.replication.lag_seconds", Unit: UnitSeconds, Nullable: true,
		SQL: `SELECT CASE WHEN pg_is_in_recovery()
			THEN GREATEST(EXTRACT(EPOCH FROM (now() - pg_last_xact_replay_timestamp())), 0)::float8
			ELSE NULL END AS value`,
	},
	{
		// PG 族特有：字节级复制延迟。MySQL 无对等概念，故留在引擎层不上升为规范指标。
		Name: "pg.replication.lag_bytes", Unit: UnitBytes, Nullable: true,
		// 主库无上游 → 无值（partial，非错误）；备库上报与上游的 WAL 差。
		// 最低兼容 PG 10（pg_last_wal_* 命名）；openGauss 走 AltSQL 的 9.2 系命名
		// （实测 openGauss-lite 5.0.3 只有 pg_last_xlog_receive_location 一族）。
		SQL: `SELECT CASE WHEN pg_is_in_recovery()
			THEN pg_wal_lsn_diff(pg_last_wal_receive_lsn(), pg_last_wal_replay_lsn())::float8
			ELSE NULL END AS value`,
		AltSQL: `SELECT CASE WHEN pg_is_in_recovery()
			THEN pg_xlog_location_diff(pg_last_xlog_receive_location(), pg_last_xlog_replay_location())::float8
			ELSE NULL END AS value`,
	},
}

// CatalogFor 按引擎族返回目录（Stage 1 仅 postgres 协议族；其余引擎 Stage 3）。
func CatalogFor(engineFamily string) []CatalogEntry {
	if engineFamily == "postgres" {
		return PostgresCatalog
	}
	return nil
}
