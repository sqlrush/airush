// Package svcapi 是服务间内部 API（spec-1.2 D2）：唯一消费方是 gateway。
// 认证 = 静态 service token（Bearer，常数时间比对）；路径前缀 /internal/v1/。
// 租户上下文来源于请求载荷（token 解析 / 证书 SAN），信任基础是 svc token +
// 载荷本身由平台签发（enrollment token / 客户端证书）——RLS 事务纪律与公开 API 一致。
package svcapi

import (
	"crypto/subtle"
	"net/http"

	"github.com/sqlrush/airush/console/internal/pki"
	"github.com/sqlrush/airush/console/internal/repo"
	"github.com/sqlrush/airush/libs/apierror"
	"github.com/sqlrush/airush/libs/metrics"
)

// Server 装配内部 API。
type Server struct {
	store    *repo.Store
	ca       *pki.CA
	svcToken string
	certTTL  certTTLConfig
	// sink/snapshotSink 是采集数据落点（spec-1.5）。nil 表示本实例未配置落库
	// ——上报请求显式 501 而不是假装收下（规则 6）。
	sink         metrics.Sink
	snapshotSink metrics.SnapshotSink
}

type certTTLConfig struct{ connectorCert int } // 天

// New 构造；svcToken 必须非空（fail fast 在 cmd 侧校验配置存在性）。
func New(store *repo.Store, ca *pki.CA, svcToken string) *Server {
	return &Server{
		store: store, ca: ca, svcToken: svcToken,
		certTTL: certTTLConfig{connectorCert: 90},
	}
}

// WithSinks 注入采集落点（spec-1.5 D5）。分开构造是为了让 spec-1.2 既有的
// 注册/握手路径在无落库配置时照常工作。
func (s *Server) WithSinks(sink metrics.Sink, snapshotSink metrics.SnapshotSink) *Server {
	s.sink, s.snapshotSink = sink, snapshotSink
	return s
}

// Handler 返回内部 API 路由（挂载在 console 同一监听端口的 /internal/v1/ 前缀）。
func (s *Server) Handler() http.Handler {
	mux := http.NewServeMux()
	handle := func(pattern string, h apierror.Handler) {
		mux.Handle(pattern, apierror.Middleware(h))
	}
	handle("POST /internal/v1/connector-enrollments", s.enroll)
	handle("POST /internal/v1/connector-handshakes", s.handshake)
	handle("POST /internal/v1/connector-status", s.status)
	// 采集数据上报（spec-1.5 D5）：gateway 收到 Connector 的 DataUpload 后转发至此。
	handle("POST /internal/v1/collected/metrics", s.ingestMetrics)
	handle("POST /internal/v1/collected/snapshots", s.ingestSnapshot)
	return s.authMiddleware(mux)
}

// authMiddleware 服务间认证；失败统一 AR_SVC_UNAUTHENTICATED。
func (s *Server) authMiddleware(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		got := r.Header.Get("Authorization")
		want := "Bearer " + s.svcToken
		if subtle.ConstantTimeCompare([]byte(got), []byte(want)) != 1 {
			apierror.WriteError(w, r.Header.Get(apierror.TraceHeader),
				apierror.New(apierror.CodeSvcUnauthenticated))
			return
		}
		next.ServeHTTP(w, r)
	})
}
