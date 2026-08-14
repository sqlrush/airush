package tsstore

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"

	"github.com/jackc/pgx/v5"

	"github.com/sqlrush/airush/console/internal/tenancy"
	"github.com/sqlrush/airush/libs/apierror"
	"github.com/sqlrush/airush/libs/metrics"
)

// PublishSnapshot 落一份快照（metrics.SnapshotSink）。
//
// 按 kind 分流两种形态——这个分流是 spec-1.5 的核心取舍之一：
//   - slowlog：每次采集内容必变（计数器在涨），哈希去重完全失效，且需要按实体做趋势，
//     故展开成读数流水进 tsdb.series；
//   - schema / config：慢变状态，整份取用，按内容哈希去重后天然成变更历史。
func (s *Store) PublishSnapshot(ctx context.Context, snap metrics.Snapshot) error {
	switch snap.Kind {
	case metrics.SnapshotKindSlowlog:
		return s.publishSlowlog(ctx, snap)
	case metrics.SnapshotKindSchema, metrics.SnapshotKindConfig:
		return s.publishStateSnapshot(ctx, snap)
	default:
		// 规则 6：未支持分支显式报错，不静默丢弃。
		return apierror.Wrap(apierror.CodeCollectUnsupportedKind,
			fmt.Errorf("tsstore: snapshot kind %q", snap.Kind))
	}
}

// publishStateSnapshot 写慢变状态快照，只在内容变化时才产生新版本。
//
// 于是这张表天然是变更历史，而不是每小时一份的重复堆积："这个库 8 月 2 号早上
// 加了个索引"两个版本 diff 一下就出来了。
func (s *Store) publishStateSnapshot(ctx context.Context, snap metrics.Snapshot) error {
	payload, hash, err := snapshotPayload(snap)
	if err != nil {
		return apierror.Wrap(apierror.CodeTimeseriesWriteFailed, err)
	}

	return s.inTenantTx(ctx, func(ctx context.Context, tx pgx.Tx) error {
		tenantID, _ := tenancy.FromContext(ctx)

		// 与当前版本比对。FOR UPDATE 防同一数据源两路采集并发时双写当前版本
		// ——唯一索引会拦住，但那是报错收场；这里让后到者等前者提交后走"未变更"分支。
		var currentID, currentHash string
		err := tx.QueryRow(ctx, `SELECT id::text, content_hash FROM collected.snapshots
			WHERE tenant_id = $1 AND datasource_id = $2 AND kind = $3 AND superseded_at IS NULL
			FOR UPDATE`,
			tenantID, snap.DatasourceID, snap.Kind).Scan(&currentID, &currentHash)
		switch {
		case err == nil && currentHash == hash:
			// 内容没变：只推进"最近一次观察到"的时间，不产生新行。
			if _, err := tx.Exec(ctx, `UPDATE collected.snapshots
				SET collected_at = GREATEST(collected_at, $2)
				WHERE tenant_id = $3 AND id = $1::uuid`,
				currentID, snap.CollectedAt, tenantID); err != nil {
				return apierror.Wrap(apierror.CodeTimeseriesWriteFailed,
					fmt.Errorf("touch snapshot %s: %w", currentID, err))
			}
			return nil
		case err == nil:
			// 内容变了：旧版本封版。
			if _, err := tx.Exec(ctx, `UPDATE collected.snapshots
				SET superseded_at = $2 WHERE tenant_id = $3 AND id = $1::uuid`,
				currentID, snap.CollectedAt, tenantID); err != nil {
				return apierror.Wrap(apierror.CodeTimeseriesWriteFailed,
					fmt.Errorf("supersede snapshot %s: %w", currentID, err))
			}
		case errIsNoRows(err):
			// 首次采集，无当前版本。
		default:
			return apierror.Wrap(apierror.CodeTimeseriesWriteFailed,
				fmt.Errorf("load current snapshot: %w", err))
		}

		if _, err := tx.Exec(ctx, `INSERT INTO collected.snapshots
			(tenant_id, datasource_id, kind, source, capability_missing, truncated,
			 catalog_version, content_hash, payload, collected_at, created_at)
			VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10, $10)`,
			tenantID, snap.DatasourceID, snap.Kind, snap.Source,
			snap.CapabilityMissing, snap.Truncated, snap.CatalogVersion,
			hash, payload, snap.CollectedAt); err != nil {
			return apierror.Wrap(apierror.CodeTimeseriesWriteFailed,
				fmt.Errorf("insert snapshot version: %w", err))
		}
		return nil
	})
}

// snapshotPayload 序列化快照内容并算内容哈希。
//
// 哈希只覆盖**内容**（表结构 / 配置项），不含 collected_at、source 这些
// 每次采集都会动的元数据——否则"没变"永远判不出来，去重形同虚设。
func snapshotPayload(snap metrics.Snapshot) ([]byte, string, error) {
	content := struct {
		Tables            []metrics.TableInfo   `json:"tables,omitempty"`
		Configs           []metrics.ConfigEntry `json:"configs,omitempty"`
		CapabilityMissing bool                  `json:"capability_missing,omitempty"`
		Truncated         bool                  `json:"truncated,omitempty"`
	}{
		Tables:            snap.Tables,
		Configs:           snap.Configs,
		CapabilityMissing: snap.CapabilityMissing,
		Truncated:         snap.Truncated,
	}
	// encoding/json 对 struct 字段按声明序、对 map 按键排序输出，故同内容必得同字节，
	// 哈希稳定。快照模型里没有 map 字段，这条前提成立。
	buf, err := json.Marshal(content)
	if err != nil {
		return nil, "", fmt.Errorf("marshal snapshot payload: %w", err)
	}
	sum := sha256.Sum256(buf)
	return buf, hex.EncodeToString(sum[:]), nil
}

func errIsNoRows(err error) bool { return errors.Is(err, pgx.ErrNoRows) }
