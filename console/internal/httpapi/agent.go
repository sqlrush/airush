package httpapi

import (
	"net/http"
	"net/http/httputil"
	"net/url"
	"strings"

	"github.com/sqlrush/airush/libs/apierror"
	"github.com/sqlrush/airush/libs/tenancy"
)

// agentProxy 是 spec-1.8 D4 的公开面：/api/v1/agent/threads* 一比一反代到 agent-runtime 的
// /internal/v1/agent/threads*，租户由本包 tenantMiddleware 注入后经 X-Airush-Tenant 头带过去，
// 服务间凭 svc token；SSE 透传（禁缓冲、按流刷）。runtime 不可达 → AR_INTERNAL（不泄露上游细节）。
type agentProxy struct {
	rp *httputil.ReverseProxy
}

// newAgentProxy 构造；baseURL 是 agent-runtime 的根（如 http://airush-agent-runtime:8082）。
func newAgentProxy(baseURL, svcToken string) (*agentProxy, error) {
	target, err := url.Parse(baseURL)
	if err != nil {
		return nil, err
	}
	rp := &httputil.ReverseProxy{
		Rewrite: func(pr *httputil.ProxyRequest) {
			pr.SetURL(target)
			pr.Out.URL.Path = strings.Replace(pr.In.URL.Path, "/api/v1/agent/", "/internal/v1/agent/", 1)
			pr.Out.URL.RawPath = ""
			pr.Out.Host = target.Host
			pr.Out.Header.Set("Authorization", "Bearer "+svcToken)
			if tenantID, ok := tenancy.FromContext(pr.In.Context()); ok {
				pr.Out.Header.Set("X-Airush-Tenant", tenantID)
			}
			// 客户端给的 Authorization / 租户头不透传：公开面的身份只来自本进程注入。
			pr.Out.Header.Del("Cookie")
		},
		// SSE：立刻刷（-1 = 不缓冲），事件到达即透传。
		FlushInterval: -1,
		ErrorHandler: func(w http.ResponseWriter, r *http.Request, _ error) {
			apierror.WriteError(w, apierror.TraceIDFrom(r), apierror.New(apierror.CodeInternalError))
		},
	}
	return &agentProxy{rp: rp}, nil
}

// ServeHTTP 实现 http.Handler。
func (p *agentProxy) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	p.rp.ServeHTTP(w, r)
}

// WithAgentRuntime 挂上 agent-runtime 反代（URL 空 = 不挂，/api/v1/agent/* 404）。
func (s *Server) WithAgentRuntime(baseURL, svcToken string) (*Server, error) {
	if baseURL == "" {
		return s, nil
	}
	p, err := newAgentProxy(baseURL, svcToken)
	if err != nil {
		return nil, err
	}
	s.agent = p
	return s, nil
}
