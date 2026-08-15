package svcapi

import (
	"context"

	"github.com/sqlrush/airush/console/internal/repo"
	"github.com/sqlrush/airush/libs/apierror"
)

// OwnershipChecker 判定「这个连接器能不能替这个数据源上报」。
//
// 租户边界不靠它——租户来自 mTLS 证书 SAN，越权写由隔离视图的 check_option 拦。
// 它守的是**租户内**的数据源归属：Connector 上报的 datasource_id 是连接器自报的，
// 被攻破的连接器不该能往同租户的其他数据源（甚至 direct 数据源）灌假数据——
// 那会污染日后 agent 诊断的依据，且在租户视角里"数据是对的、只是从哪来的不对"最难察觉。
type OwnershipChecker interface {
	// Check 在租户上下文中执行；归属不符返回 AR_COLLECT_DATASOURCE_MISMATCH，
	// 数据源不存在返回 AR_DATASOURCE_NOT_FOUND（两者都是 fail-closed）。
	Check(ctx context.Context, datasourceID, connectorID string) error
}

// repoOwnership 是生产实现：查 datasources 表（RLS 视图内），由数据库回答归属。
type repoOwnership struct{ store *repo.Store }

func (o repoOwnership) Check(ctx context.Context, datasourceID, connectorID string) error {
	return o.store.InTenantTx(ctx, func(ctx context.Context, tx repo.Tx) error {
		ds, err := repo.GetDatasource(ctx, tx, datasourceID)
		if err != nil {
			return err // 查无 → AR_DATASOURCE_NOT_FOUND，原样冒泡
		}
		if ds.ConnectMode != "connector" || ds.ConnectorID == nil || *ds.ConnectorID != connectorID {
			return apierror.New(apierror.CodeCollectDatasourceMismatch)
		}
		return nil
	})
}
