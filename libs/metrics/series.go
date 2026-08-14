package metrics

import (
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
)

// series 注册表（spec-1.5 D2）：把"什么能落进 tsdb.series"这件事收口成编译期声明。
//
// 泛化让表数固定住了，代价是 entity_id 成了一个能塞任意文本的口子——而 AD-3 的底线
// 是客户业务数据绝不进平台。旧防线是 label 键白名单（allowedLabelKeys），泛化后不够用；
// 新防线是**目录声明**：一条 series 带不带实体、实体是什么，必须在编译期写死，
// 运行期出现未声明的实体一律显式拒绝（规则 6：不静默丢弃）。

// 实体类型白名单。新增一类必须同时新增 spec 的论证（尤其是基数上限——
// 高基数实体会让压缩 segment 数爆炸，见 spec-1.5 R8）。
const (
	// EntityKindQuery 是慢查询 SQL 实体，entity_id = sha256(规范化文本) 前 16 字节。
	EntityKindQuery = "query"
)

// 慢查询度量的 series 名（spec-1.5 §2.2：一条慢 SQL 的 5 个度量拆成 5 条 series）。
//
// 单位取**秒**而非 spec §2.2 示例里写的毫秒（该处示例与 Unit 词表不自洽，
// 词表无毫秒项；已追加 spec changelog）。规范层单位统一到 SI 才叫规范：
// PG 的 pg_stat_statements 给毫秒、MySQL 的慢日志给秒，换算在采集侧消化一次，
// 下游 skill 与图表永远不必判断"这条是毫秒还是秒"。
const (
	SeriesSlowlogCalls    = "db.slowlog.calls"
	SeriesSlowlogTotalSec = "db.slowlog.total_seconds"
	SeriesSlowlogMeanSec  = "db.slowlog.mean_seconds"
	SeriesSlowlogMaxSec   = "db.slowlog.max_seconds"
	SeriesSlowlogRows     = "db.slowlog.rows"
)

// msPerSecond 是慢查询耗时由毫秒换算为规范单位（秒）的除数。
const msPerSecond = 1000.0

// SlowlogSeries 是慢查询快照展开成的 series 声明，顺序稳定（写入与测试遍历用）。
var SlowlogSeries = []CatalogEntry{
	{Name: SeriesSlowlogCalls, Unit: UnitCount, EntityKind: EntityKindQuery},
	{Name: SeriesSlowlogTotalSec, Unit: UnitSeconds, EntityKind: EntityKindQuery},
	{Name: SeriesSlowlogMeanSec, Unit: UnitSeconds, EntityKind: EntityKindQuery},
	{Name: SeriesSlowlogMaxSec, Unit: UnitSeconds, EntityKind: EntityKindQuery},
	{Name: SeriesSlowlogRows, Unit: UnitCount, EntityKind: EntityKindQuery},
}

// SlowQuerySeriesValues 把一条慢查询统计展开成 (series 名, 值) 对，
// 顺序与 SlowlogSeries 一致。耗时类在此处完成毫秒→秒换算，是唯一换算点。
func SlowQuerySeriesValues(e SlowQueryEntry) [5]struct {
	Name  string
	Value float64
} {
	return [5]struct {
		Name  string
		Value float64
	}{
		{SeriesSlowlogCalls, float64(e.Calls)},
		{SeriesSlowlogTotalSec, e.TotalMs / msPerSecond},
		{SeriesSlowlogMeanSec, e.MeanMs / msPerSecond},
		{SeriesSlowlogMaxSec, e.MaxMs / msPerSecond},
		{SeriesSlowlogRows, float64(e.Rows)},
	}
}

// ErrUndeclaredSeries 表示 series 名不在编译期目录里。
var ErrUndeclaredSeries = errors.New("metrics: undeclared series name")

// ErrUndeclaredEntity 表示某条 series 携带了它未声明的实体维度（AD-3 防线）。
var ErrUndeclaredEntity = errors.New("metrics: series carries undeclared entity")

// seriesRegistry 汇总全部引擎目录 + 快照展开的 series，供运行期校验。
// 包级 var 在 init 前完成构造（依赖同包 var，Go 保证初始化顺序按依赖解析）。
var seriesRegistry = buildSeriesRegistry()

func buildSeriesRegistry() map[string]CatalogEntry {
	reg := make(map[string]CatalogEntry, len(PostgresCatalog)+len(SlowlogSeries))
	for _, group := range [][]CatalogEntry{PostgresCatalog, SlowlogSeries} {
		for _, entry := range group {
			reg[entry.Name] = entry
		}
	}
	return reg
}

// LookupSeries 返回 series 的编译期声明。
func LookupSeries(name string) (CatalogEntry, bool) {
	entry, ok := seriesRegistry[name]
	return entry, ok
}

// ValidateSeriesEntity 校验一条待落库读数的 (series, entity) 组合：
//   - series 必须已声明；
//   - 带实体时该 series 必须声明了 EntityKind；
//   - 声明了 EntityKind 的 series 必须带实体（否则读数无从归属，是采集侧 bug）。
//
// 返回错误而非静默丢弃——静默丢弃会让 AD-3 违规悄悄发生（规则 6）。
func ValidateSeriesEntity(seriesName, entityID string) error {
	entry, ok := LookupSeries(seriesName)
	if !ok {
		return fmt.Errorf("%w: %s", ErrUndeclaredSeries, seriesName)
	}
	switch {
	case entityID != "" && entry.EntityKind == "":
		return fmt.Errorf("%w: %s 未声明 EntityKind 却携带 entity_id", ErrUndeclaredEntity, seriesName)
	case entityID == "" && entry.EntityKind != "":
		return fmt.Errorf("%w: %s 声明了 EntityKind=%s 却无 entity_id",
			ErrUndeclaredEntity, seriesName, entry.EntityKind)
	}
	return nil
}

// entityIDLen 是实体 ID 的十六进制长度（8 字节 = 16 hex 字符）。
// 64 位空间对单个数据源的 SQL 规模（万级）碰撞概率可忽略，且比全长 sha256 省
// 每行 48 字节——entity_id 在 compress_segmentby 里，长度直接进每个 segment 头。
const entityIDLen = 16

// EntityIDFor 由实体的规范化文本算出稳定 ID。
//
// 为什么不用引擎给的 queryid / unique_sql_id：那些值实例重启会变、跨实例不可比，
// 而同一条 SQL 在主备两个实例上**应当是同一个实体**——只有内容哈希能做到。
// 引擎原生标识另存 entities.native_id 供排障对照，不做主键。
func EntityIDFor(normalizedText string) string {
	sum := sha256.Sum256([]byte(normalizedText))
	return hex.EncodeToString(sum[:])[:entityIDLen]
}
