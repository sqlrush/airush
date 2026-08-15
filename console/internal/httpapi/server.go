// Package httpapi 是控制面 REST API（spec-1.1 D2/D4）。
// 契约：proto/openapi/console.yaml；错误响应走 libs/apierror 中间件；
// 数据访问只经 internal/repo 基座（depguard 硬禁直连 pgx）。
package httpapi

import (
	"context"
	"fmt"
	"net/http"

	"github.com/sqlrush/airush/console/internal/credcrypto"
	"github.com/sqlrush/airush/console/internal/directconn"
	"github.com/sqlrush/airush/console/internal/repo"
	"github.com/sqlrush/airush/console/internal/tenancy"
	"github.com/sqlrush/airush/libs/apierror"
)

// Server 装配全部路由；依赖经构造注入。
type Server struct {
	store           *repo.Store
	sealer          *credcrypto.Sealer
	directConn      DirectTester
	defaultTenantID string
	// collected 是采集数据只读查询面（spec-1.5 D4）；nil 时相关路由返回 501。
	collected CollectedReader
}

// DirectTester 是直连测试面（spec-1.17 directconn.Manager 满足）；接口化便于测试替身
// 且避免 httpapi 直接耦合连接池实现。
type DirectTester interface {
	TestConnection(ctx context.Context, datasourceID string) (directconn.TestResult, error)
}

// New 构造 Server；defaultTenantID 必须是合法 UUID（Stage 1 租户来源，spec-2.2 换认证态）。
// directConn 为 nil 时 test-connection 返回 AR_COMMON_NOT_IMPLEMENTED。
func New(store *repo.Store, sealer *credcrypto.Sealer, directConn DirectTester, defaultTenantID string) (*Server, error) {
	if !isUUID(defaultTenantID) {
		return nil, fmt.Errorf("httpapi: default tenant id %q is not a UUID", defaultTenantID)
	}
	return &Server{store: store, sealer: sealer, directConn: directConn, defaultTenantID: defaultTenantID}, nil
}

// WithCollected 注入采集数据查询面（spec-1.5 D4）。分开注入是为了让 spec-1.1 既有
// 路由在无时序存储的形态（如仅跑迁移的进程）下照常构造。
func (s *Server) WithCollected(r CollectedReader) *Server {
	s.collected = r
	return s
}

// Handler 返回带租户注入的 API 根 handler（观测中间件由 cmd 侧统一包裹）。
func (s *Server) Handler() http.Handler {
	mux := http.NewServeMux()
	handle := func(pattern string, h apierror.Handler) {
		mux.Handle(pattern, apierror.Middleware(h))
	}

	handle("POST /api/v1/datasources", s.createDatasource)
	handle("GET /api/v1/datasources", s.listDatasources)
	handle("GET /api/v1/datasources/{id}", s.getDatasource)
	handle("PATCH /api/v1/datasources/{id}", s.patchDatasource)
	handle("DELETE /api/v1/datasources/{id}", s.deleteDatasource)
	handle("PUT /api/v1/datasources/{id}/credential", s.putCredential)
	handle("POST /api/v1/datasources/{id}/test-connection", s.testConnection)

	handle("GET /api/v1/datasources/{id}/aliases", s.listAliases)
	handle("POST /api/v1/datasources/{id}/aliases", s.createAlias)
	handle("DELETE /api/v1/datasources/{id}/aliases/{alias}", s.deleteAlias)

	handle("POST /api/v1/agents", s.createAgent)
	handle("GET /api/v1/agents", s.listAgents)
	handle("GET /api/v1/agents/{id}", s.getAgent)
	handle("PATCH /api/v1/agents/{id}", s.patchAgent)
	handle("DELETE /api/v1/agents/{id}", s.deleteAgent)

	handle("POST /api/v1/datasource-groups", s.createGroup)
	handle("GET /api/v1/datasource-groups", s.listGroups)
	handle("GET /api/v1/datasource-groups/{id}", s.getGroup)
	handle("PATCH /api/v1/datasource-groups/{id}", s.patchGroup)
	handle("DELETE /api/v1/datasource-groups/{id}", s.deleteGroup)

	handle("GET /api/v1/connectors", s.listConnectors)
	handle("GET /api/v1/connectors/{id}", s.getConnector)
	handle("POST /api/v1/connectors", s.createConnector)
	handle("POST /api/v1/connectors/{id}/revoke", s.revokeConnector)

	// 采集数据只读面（spec-1.5 D4）：spec-1.10/1.11/1.12/1.13 的数据入口。
	handle("GET /api/v1/datasources/{id}/series", s.seriesRange)
	handle("GET /api/v1/datasources/{id}/top-entities", s.topEntities)
	handle("GET /api/v1/datasources/{id}/snapshots/{kind}", s.latestSnapshot)
	handle("GET /api/v1/datasources/{id}/snapshots/{kind}/history", s.snapshotHistory)

	return s.tenantMiddleware(mux)
}

// tenantMiddleware 把默认租户注入请求 ctx（唯一注入点；认证接管见 spec-2.2）。
func (s *Server) tenantMiddleware(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		ctx := tenancy.WithTenant(r.Context(), s.defaultTenantID)
		next.ServeHTTP(w, r.WithContext(ctx))
	})
}
