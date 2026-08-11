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
)

// Server 装配内部 API。
type Server struct {
	store    *repo.Store
	ca       *pki.CA
	svcToken string
	certTTL  certTTLConfig
}

type certTTLConfig struct{ connectorCert int } // 天

// New 构造；svcToken 必须非空（fail fast 在 cmd 侧校验配置存在性）。
func New(store *repo.Store, ca *pki.CA, svcToken string) *Server {
	return &Server{
		store: store, ca: ca, svcToken: svcToken,
		certTTL: certTTLConfig{connectorCert: 90},
	}
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
