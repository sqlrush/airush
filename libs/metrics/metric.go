// Package metrics 是通道无关的指标采集探针与模型（spec-1.3 D1/D2）。
// Direct（directconn 连接池）与 Connector（客户侧连接）两通道共享同一探针，
// 只提供 Querier——兑现 spec-1.17/1.2 §10 的"一套探针两通道"承诺（AD-3 只结构化指标）。
package metrics

import "time"

// CatalogVersion 随每个 Batch 上报，供 spec-1.5 时序 schema 演进对齐。
// 目录只增指标、不改已发布指标语义时不 bump；语义变更（单位/含义）才 bump。
const CatalogVersion = 1

// Unit 是指标单位（受控枚举）。
const (
	UnitCount   = "count"
	UnitRatio   = "ratio"
	UnitBytes   = "bytes"
	UnitSeconds = "seconds"
)

// Metric 是一条结构化指标（AD-3：聚合系统视图，零行级客户数据）。
type Metric struct {
	Name   string            `json:"name"`
	Value  float64           `json:"value"`
	Unit   string            `json:"unit"`
	Labels map[string]string `json:"labels,omitempty"`
	At     time.Time         `json:"at"`
}

// Batch 是一次采集产出的指标集。
type Batch struct {
	DatasourceID   string    `json:"datasource_id"`
	EngineFamily   string    `json:"engine_family"`
	CatalogVersion int       `json:"catalog_version"`
	Metrics        []Metric  `json:"metrics"`
	CollectedAt    time.Time `json:"collected_at"`
	// Partial 为 true 表示部分指标 SQL 失败缺采（其余照采，spec-1.3 §2.2）。
	Partial bool `json:"partial"`
	// Missing 记缺采的指标名（可观测性；不含错误细节）。
	Missing []string `json:"missing,omitempty"`
}

// allowedLabelKeys 是 Metric.Labels 的白名单（AD-3 防高基数/防原始数据，spec-1.3 §3）。
// 非白名单键在探针构造期被丢弃并计入告警——绝不让 query 文本/行数据混入 label。
var allowedLabelKeys = map[string]struct{}{
	"datasource_id": {},
	"database":      {},
	"engine":        {},
}

// AllowedLabelKey 报告某 label 键是否在白名单内。
func AllowedLabelKey(key string) bool {
	_, ok := allowedLabelKeys[key]
	return ok
}

// sanitizeLabels 剔除非白名单键，返回被剔除的键（供告警）。
func sanitizeLabels(labels map[string]string) (map[string]string, []string) {
	if len(labels) == 0 {
		return nil, nil
	}
	clean := make(map[string]string, len(labels))
	var dropped []string
	for k, v := range labels {
		if AllowedLabelKey(k) {
			clean[k] = v
		} else {
			dropped = append(dropped, k)
		}
	}
	if len(clean) == 0 {
		clean = nil
	}
	return clean, dropped
}
