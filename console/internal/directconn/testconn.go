package directconn

import (
	"context"
	"errors"
	"time"

	"github.com/sqlrush/airush/libs/apierror"
)

// TestResult 是连接测试结果（只读，不落库，不改 health_status；spec-1.17 §8 Q4）。
type TestResult struct {
	OK      bool   `json:"ok"`
	Version string `json:"server_version"`
}

// TestConnection 直连数据库跑 SELECT version() 校验连通性。超时 → TEST_TIMEOUT；
// 建连/认证失败 → CONNECT_FAILED。凭据明文不出现在任何返回/日志。
func (m *Manager) TestConnection(ctx context.Context, datasourceID string) (TestResult, error) {
	pool, err := m.poolFor(ctx, datasourceID)
	if err != nil {
		return TestResult{}, err
	}

	probeCtx, cancel := context.WithTimeout(ctx, m.effectiveTimeout())
	defer cancel()

	var version string
	if err := pool.QueryRow(probeCtx, "SELECT version()").Scan(&version); err != nil {
		if errors.Is(err, context.DeadlineExceeded) {
			return TestResult{}, apierror.New(apierror.CodeDatasourceTestTimeout)
		}
		// 建连成功但查询失败（权限/协议）也归连接失败类，不泄漏细节
		m.Destroy(datasourceID) // 疑似坏连接，弃池下次重建
		return TestResult{}, connectFailed(err)
	}
	return TestResult{OK: true, Version: version}, nil
}

// Ensure the timeout constant is referenced even if ConnectTimeout is zero-value
// in a misconfigured Manager (defensive default).
func (m *Manager) effectiveTimeout() time.Duration {
	if m.cfg.ConnectTimeout <= 0 {
		return DefaultConfig().ConnectTimeout
	}
	return m.cfg.ConnectTimeout
}
