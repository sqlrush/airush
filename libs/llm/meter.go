package llm

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"strings"
	"sync/atomic"
	"time"

	"github.com/sqlrush/airush/libs/apierror"
	"github.com/sqlrush/airush/libs/tenancy"
)

// CallInfo 是一次调用的归属信息（租户之外的部分）；租户始终从 tenancy ctx 取。
type CallInfo struct {
	AgentID   string
	SessionID string
	TraceID   string
	Purpose   string
}

type callInfoKey struct{}

// WithCallInfo 把归属信息挂进 ctx（不修改传入 ctx）。
func WithCallInfo(ctx context.Context, ci CallInfo) context.Context {
	return context.WithValue(ctx, callInfoKey{}, ci)
}

// CallInfoFrom 取归属信息；未挂则零值。
func CallInfoFrom(ctx context.Context) CallInfo {
	ci, _ := ctx.Value(callInfoKey{}).(CallInfo)
	return ci
}

// QuotaGate 在调用前回答"还能不能调"。超额返回 AR_QUOTA_EXCEEDED；
// 其它错误（控制面不可达）由 Meter 按 fail-open 处理（spec-1.7 §3.1）。
type QuotaGate interface {
	Check(ctx context.Context, tenantID string) error
}

// Recorder 记一次用量。status ∈ ok / upstream_error / quota_rejected / aborted。
// idemKey 是 Meter 生成的幂等键，重试不双记。
type Recorder interface {
	Record(ctx context.Context, tenantID string, u Usage, status, idemKey string) error
}

// Status 常量与 llm_usage.status CHECK 一致。
const (
	StatusOK            = "ok"
	StatusUpstreamError = "upstream_error"
	StatusQuotaRejected = "quota_rejected"
	StatusAborted       = "aborted"
)

// 注入到网关请求的头（供 LiteLLM json 日志关联；不含任何用户内容）。
const (
	HeaderTenant  = "X-Airush-Tenant"
	HeaderAgent   = "X-Airush-Agent"
	HeaderSession = "X-Airush-Session"
	HeaderTrace   = "X-Airush-Trace"
)

// Meter 是挂在 OpenAI 兼容客户端 Transport 上的 RoundTripper（spec-1.7 D3）。
type Meter struct {
	next   http.RoundTripper
	gate   QuotaGate
	rec    Recorder
	key    string // master key；由 Meter 统一加 Authorization，调用方代码不接触
	logger *slog.Logger
	// QuotaCheckFailures 计"配额门不可达但放行"的次数（fail-open 的可见性，R5）。
	QuotaCheckFailures atomic.Int64
	seq                atomic.Int64
	now                func() time.Time
}

// Option 配置 Meter。
type Option func(*Meter)

// WithLogger 设日志器（缺省丢弃）。
func WithLogger(l *slog.Logger) Option { return func(m *Meter) { m.logger = l } }

// WithMasterKey 让 Meter 统一注入 Authorization: Bearer <key>。
func WithMasterKey(k string) Option { return func(m *Meter) { m.key = k } }

// NewMeter 构造。next 为 nil 时用 http.DefaultTransport。gate/rec 不得为 nil——
// 没有配额门或记账器就等于放弃这道防线，宁可构造期 panic 也不静默降级。
func NewMeter(next http.RoundTripper, gate QuotaGate, rec Recorder, opts ...Option) *Meter {
	if gate == nil || rec == nil {
		panic("llm.NewMeter: QuotaGate 与 Recorder 不得为 nil")
	}
	if next == nil {
		next = http.DefaultTransport
	}
	m := &Meter{
		next: next, gate: gate, rec: rec,
		logger: slog.New(slog.NewTextHandler(io.Discard, nil)), now: time.Now,
	}
	for _, o := range opts {
		o(m)
	}
	return m
}

// RoundTrip 实现 http.RoundTripper。
func (m *Meter) RoundTrip(req *http.Request) (*http.Response, error) {
	ctx := req.Context()
	tenantID, ok := tenancy.FromContext(ctx)
	if !ok {
		// 无租户就不出网——记不了账的调用不该发生（fail-closed，与落库层同口径）。
		return nil, apierror.New(apierror.CodeTenantContextMissing)
	}
	ci := CallInfoFrom(ctx)
	idem := m.idemKey(ci.TraceID)
	start := m.now()

	// 配额门：超额拒；控制面不可达放行并计数（fail-open，配额是成本护栏不是安全边界）。
	if err := m.gate.Check(ctx, tenantID); err != nil {
		var ae *apierror.Error
		if errors.As(err, &ae) && ae.Code == apierror.CodeQuotaExceeded {
			m.record(ctx, tenantID, Usage{Model: modelOf(req)}, StatusQuotaRejected, idem, start, err)
			return nil, err
		}
		m.QuotaCheckFailures.Add(1)
		llmQuotaCheckFailed.Add(ctx, 1)
		m.logger.Warn("llm quota check unavailable, fail-open", "err", err, "tenant_id", tenantID)
	}

	req = m.prepare(req, tenantID, ci)
	logical := modelOf(req)

	resp, err := m.next.RoundTrip(req)
	if err != nil {
		wrapped := apierror.Wrap(apierror.CodeUpstreamLlmFailed, err)
		m.record(ctx, tenantID, Usage{Model: logical}, StatusUpstreamError, idem, start, wrapped)
		return nil, wrapped
	}
	if resp.StatusCode >= 400 {
		return nil, m.mapError(ctx, resp, tenantID, logical, idem, start)
	}
	return m.wrapResponse(ctx, resp, tenantID, logical, idem, start)
}

// wrapResponse 在成功响应上挂用量提取：流式包一层 tee 读到末帧再记；非流式读完正文记完再原样装回。
func (m *Meter) wrapResponse(ctx context.Context, resp *http.Response, tenantID, logical, idem string, start time.Time) (*http.Response, error) {
	cost := parseCostHeader(resp.Header.Get("x-litellm-response-cost"))
	if isSSE(resp.Header.Get("Content-Type")) {
		resp.Body = &teeUsageReader{
			src: resp.Body, scanner: &sseUsageScanner{},
			done: func(u Usage, complete bool) {
				u.Model, u.CostRefMicro = logical, cost
				status := StatusOK
				if !complete {
					// 断流：末帧 usage 没到，token 记 0 但计一次调用（§3.3）
					u = Usage{Model: logical, Stream: true, CostRefMicro: cost}
					status = StatusAborted
				}
				m.record(context.WithoutCancel(ctx), tenantID, u, status, idem, start, nil)
			},
		}
		return resp, nil
	}

	// 非流式：读出正文取 usage，再把正文原样装回去给调用方。
	body, err := io.ReadAll(resp.Body)
	_ = resp.Body.Close()
	if err != nil {
		wrapped := apierror.Wrap(apierror.CodeUpstreamLlmFailed, fmt.Errorf("read response: %w", err))
		m.record(ctx, tenantID, Usage{Model: logical}, StatusUpstreamError, idem, start, wrapped)
		return nil, wrapped
	}
	resp.Body = io.NopCloser(bytes.NewReader(body))
	u, found := extractJSONUsage(body)
	u.Model, u.CostRefMicro = logical, cost
	if !found {
		m.logger.Warn("llm response without usage", "model", logical)
	}
	m.record(ctx, tenantID, u, StatusOK, idem, start, nil)
	return resp, nil
}

// prepare 复制请求：注入头、Authorization、流式请求补 include_usage。
// 不改调用方的 *http.Request（RoundTripper 契约）。
func (m *Meter) prepare(req *http.Request, tenantID string, ci CallInfo) *http.Request {
	r := req.Clone(req.Context())
	r.Header.Set(HeaderTenant, tenantID)
	if ci.AgentID != "" {
		r.Header.Set(HeaderAgent, ci.AgentID)
	}
	if ci.SessionID != "" {
		r.Header.Set(HeaderSession, ci.SessionID)
	}
	if ci.TraceID != "" {
		r.Header.Set(HeaderTrace, ci.TraceID)
	}
	if m.key != "" {
		r.Header.Set("Authorization", "Bearer "+m.key)
	}
	if req.Body == nil || req.Body == http.NoBody {
		return r
	}
	body, err := io.ReadAll(req.Body)
	_ = req.Body.Close()
	if err != nil {
		r.Body = io.NopCloser(bytes.NewReader(nil))
		return r
	}
	body = ensureIncludeUsage(body)
	r.Body = io.NopCloser(bytes.NewReader(body))
	r.ContentLength = int64(len(body))
	r.GetBody = func() (io.ReadCloser, error) { return io.NopCloser(bytes.NewReader(body)), nil }
	// 后面 modelOf 还要读，也让 next 能重放
	req.Body = io.NopCloser(bytes.NewReader(body))
	return r
}

// ensureIncludeUsage：请求是流式且未声明 stream_options 时补 {"include_usage":true}——
// 否则 OpenAI 语义下流式响应不带 usage，记账就成了瞎子。已声明的原样尊重。
func ensureIncludeUsage(body []byte) []byte {
	var m map[string]json.RawMessage
	if err := json.Unmarshal(body, &m); err != nil {
		return body
	}
	var stream bool
	if raw, ok := m["stream"]; ok {
		_ = json.Unmarshal(raw, &stream)
	}
	if !stream {
		return body
	}
	if _, has := m["stream_options"]; has {
		return body
	}
	m["stream_options"] = json.RawMessage(`{"include_usage":true}`)
	out, err := json.Marshal(m)
	if err != nil {
		return body
	}
	return out
}

// modelOf 从请求体取逻辑模型名（body 已由 prepare 重放为可重复读）。
func modelOf(req *http.Request) string {
	if req.Body == nil || req.Body == http.NoBody {
		return ""
	}
	if req.GetBody != nil {
		rc, err := req.GetBody()
		if err == nil {
			defer func() { _ = rc.Close() }()
			var m struct {
				Model string `json:"model"`
			}
			_ = json.NewDecoder(rc).Decode(&m)
			return m.Model
		}
	}
	// 无 GetBody（第一次 prepare 前）：peek 后放回
	body, err := io.ReadAll(req.Body)
	if err != nil {
		return ""
	}
	req.Body = io.NopCloser(bytes.NewReader(body))
	var m struct {
		Model string `json:"model"`
	}
	_ = json.Unmarshal(body, &m)
	return m.Model
}

// mapError 把网关 4xx/5xx 映射成我们的错误码；上游正文只进日志，不透传（R6）。
func (m *Meter) mapError(ctx context.Context, resp *http.Response, tenantID, logical, idem string, start time.Time) error {
	body, _ := io.ReadAll(io.LimitReader(resp.Body, 4096))
	_ = resp.Body.Close()
	m.logger.Warn("llm gateway error", "status", resp.StatusCode, "model", logical,
		"upstream_body_len", len(body))

	var mapped error
	switch {
	case resp.StatusCode == http.StatusBadRequest && strings.Contains(string(body), "Invalid model name"):
		mapped = apierror.New(apierror.CodeUpstreamLlmModelUnknown)
	case resp.StatusCode == http.StatusGatewayTimeout || resp.StatusCode == http.StatusRequestTimeout:
		mapped = apierror.New(apierror.CodeUpstreamLlmTimeout)
	default:
		// 含 429（上游限流）与其余 5xx
		mapped = apierror.Wrap(apierror.CodeUpstreamLlmFailed, fmt.Errorf("gateway http %d", resp.StatusCode))
	}
	m.record(ctx, tenantID, Usage{Model: logical}, StatusUpstreamError, idem, start, mapped)
	return mapped
}

// record 记账；失败只记日志（Recorder 自带重试与兜底日志，见 console 实现），
// 绝不让记账失败反向影响调用。
func (m *Meter) record(ctx context.Context, tenantID string, u Usage, status, idem string, start time.Time, callErr error) {
	observeCall(ctx, u.Model, start, u, status, callErr)
	if err := m.rec.Record(ctx, tenantID, u, status, idem); err != nil {
		m.logger.Error("llm usage record failed", "err", err, "tenant_id", tenantID,
			"model", u.Model, "status", status, "idem_key", idem)
	}
}

// idemKey = trace_id + 进程内序号；无 trace 时用时间戳纳秒兜底（仍唯一，只是不可关联）。
func (m *Meter) idemKey(traceID string) string {
	n := m.seq.Add(1)
	if traceID == "" {
		return fmt.Sprintf("notrace-%d-%d", m.now().UnixNano(), n)
	}
	return fmt.Sprintf("%s-%d", traceID, n)
}
