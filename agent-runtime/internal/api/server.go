// Package api 是 agent-runtime 的内部 HTTP 面（spec-1.8 §2.5）：svc token 认证，租户由
// `X-Airush-Tenant` 头给出（console 公开面的默认租户中间件注入后一比一反代），SSE 事件流。
package api

import (
	"bytes"
	"crypto/subtle"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"regexp"

	"github.com/sqlrush/codexgo/pkg/threadstore"

	"github.com/sqlrush/airush/agent-runtime/internal/runtime"
	"github.com/sqlrush/airush/libs/apierror"
	"github.com/sqlrush/airush/libs/tenancy"
)

// HeaderTenant 是内部 API 的租户头（与 libs/llm 网关头同名，一处口径）。
const HeaderTenant = "X-Airush-Tenant"

// maxBodyBytes 是请求体上限（一轮输入 1MB 足够；图片走对象存储是 Stage 4）。
const maxBodyBytes = 1 << 20

var uuidRe = regexp.MustCompile(`^[0-9a-fA-F]{8}-[0-9a-fA-F]{4}-[0-9a-fA-F]{4}-[0-9a-fA-F]{4}-[0-9a-fA-F]{12}$`)

// Server 装配路由。
type Server struct {
	core     runtime.AgentCore
	engine   *runtime.Engine
	svcToken string
}

// New 构造；svcToken 必须非空（cmd 侧校验配置存在性）。engine 可为 nil（纯 core 接口测试），
// 此时列表/历史类只读端点不可用。
func New(core runtime.AgentCore, engine *runtime.Engine, svcToken string) *Server {
	return &Server{core: core, engine: engine, svcToken: svcToken}
}

// Handler 返回带认证与租户注入的根 handler（观测中间件由 cmd 侧包裹）。
func (s *Server) Handler() http.Handler {
	mux := http.NewServeMux()
	handle := func(pattern string, h apierror.Handler) {
		mux.Handle(pattern, apierror.Middleware(h))
	}
	handle("POST /internal/v1/agent/threads", s.createThread)
	handle("GET /internal/v1/agent/threads", s.listThreads)
	handle("GET /internal/v1/agent/threads/{id}", s.getThread)
	handle("DELETE /internal/v1/agent/threads/{id}", s.deleteThread)
	handle("POST /internal/v1/agent/threads/{id}/turns", s.submitTurn)
	handle("POST /internal/v1/agent/threads/{id}/interrupt", s.interrupt)
	handle("POST /internal/v1/agent/threads/{id}/resume", s.resume)
	handle("GET /internal/v1/agent/threads/{id}/events", s.events)
	handle("GET /internal/v1/agent/threads/{id}/items", s.listItems)
	return s.authMiddleware(s.tenantMiddleware(mux))
}

// authMiddleware 服务间认证；失败统一 AR_SVC_UNAUTHENTICATED。
func (s *Server) authMiddleware(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		want := "Bearer " + s.svcToken
		if subtle.ConstantTimeCompare([]byte(r.Header.Get("Authorization")), []byte(want)) != 1 {
			apierror.WriteError(w, apierror.TraceIDFrom(r), apierror.New(apierror.CodeSvcUnauthenticated))
			return
		}
		next.ServeHTTP(w, r)
	})
}

// tenantMiddleware 把 X-Airush-Tenant 注入 ctx；缺失/非 UUID → AR_TENANT_CONTEXT_MISSING（fail-closed）。
func (s *Server) tenantMiddleware(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		tenantID := r.Header.Get(HeaderTenant)
		if !uuidRe.MatchString(tenantID) {
			apierror.WriteError(w, apierror.TraceIDFrom(r), apierror.New(apierror.CodeTenantContextMissing))
			return
		}
		next.ServeHTTP(w, r.WithContext(tenancy.WithTenant(r.Context(), tenantID)))
	})
}

// pathUUID 取路径参数并校验 UUID 形态。
func pathUUID(r *http.Request, name string) (string, error) {
	v := r.PathValue(name)
	if !uuidRe.MatchString(v) {
		return "", apierror.New(apierror.CodeValidationFailed).WithDetails(apierror.Detail{Field: name, Reason: "必须是 UUID"})
	}
	return v, nil
}

// decodeBody 限额读取并严格解码（未知字段即 400）。
func decodeBody(r *http.Request, dst any) error {
	body, err := io.ReadAll(io.LimitReader(r.Body, maxBodyBytes+1))
	if err != nil {
		return fmt.Errorf("read request body: %w", err)
	}
	if len(body) > maxBodyBytes {
		return apierror.New(apierror.CodeValidationFailed).WithDetails(apierror.Detail{Field: "body", Reason: "请求体超过 1MB 上限"})
	}
	if len(bytes.TrimSpace(body)) == 0 {
		return nil
	}
	dec := json.NewDecoder(bytes.NewReader(body))
	dec.DisallowUnknownFields()
	if err := dec.Decode(dst); err != nil {
		return apierror.Wrap(apierror.CodeValidationFailed, err).WithDetails(apierror.Detail{Field: "body", Reason: "JSON 解析失败或含未知字段"})
	}
	return nil
}

func writeJSON(w http.ResponseWriter, status int, v any) error {
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.WriteHeader(status)
	if err := json.NewEncoder(w).Encode(v); err != nil {
		return fmt.Errorf("encode response: %w", err)
	}
	return nil
}

// mapErr 把 codexgo threadstore 错误映射成平台错误码；apierror 原样透传。
func mapErr(err error) error {
	if err == nil {
		return nil
	}
	if _, ok := apierror.FromError(err); ok {
		return err
	}
	var se *threadstore.Error
	if errors.As(err, &se) {
		switch se.Kind {
		case threadstore.ErrorKindThreadNotFound:
			return apierror.Wrap(apierror.CodeAgentThreadNotFound, err)
		case threadstore.ErrorKindInvalidRequest, threadstore.ErrorKindUnsupported:
			return apierror.Wrap(apierror.CodeValidationFailed, err)
		}
	}
	return apierror.Wrap(apierror.CodeInternalError, err)
}
