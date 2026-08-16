package llm

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"time"

	"github.com/sqlrush/airush/libs/apierror"
)

// ConsoleClient 是 QuotaGate + Recorder 的生产实现：经 console 内部 API（svc token）
// 查额与记账（spec-1.7 §2.5）。DB 访问只在 console——与 gateway→console 同一模式。
type ConsoleClient struct {
	baseURL  string
	svcToken string
	http     *http.Client
	// recordRetries 是记账失败的重试次数（§3.1：3 次后落日志由调用方兜底）
	recordRetries int
	sleep         func(time.Duration)
}

// NewConsoleClient 构造。baseURL 形如 http://airush-console:8080。
func NewConsoleClient(baseURL, svcToken string) *ConsoleClient {
	return &ConsoleClient{
		baseURL: baseURL, svcToken: svcToken,
		http:          &http.Client{Timeout: 5 * time.Second},
		recordRetries: 3,
		sleep:         time.Sleep,
	}
}

var (
	_ QuotaGate = (*ConsoleClient)(nil)
	_ Recorder  = (*ConsoleClient)(nil)
)

// quotaCheckResponse 是 /internal/v1/llm/quota-check 的 200 体。
type quotaCheckResponse struct {
	Budget          int64 `json:"budget"`
	Used            int64 `json:"used"`
	RemainingTokens int64 `json:"remaining_tokens"`
	HardStop        bool  `json:"hard_stop"`
}

// Check 实现 QuotaGate：429 → AR_QUOTA_EXCEEDED；其它 HTTP 错误原样返回（Meter fail-open）。
func (c *ConsoleClient) Check(ctx context.Context, tenantID string) error {
	status, body, err := c.post(ctx, "/internal/v1/llm/quota-check", map[string]any{"tenant_id": tenantID})
	if err != nil {
		return err
	}
	switch status {
	case http.StatusOK:
		return nil
	case http.StatusTooManyRequests:
		return apierror.New(apierror.CodeQuotaExceeded)
	default:
		return fmt.Errorf("quota-check http %d: %s", status, truncate(body, 200))
	}
}

// usageRequest 是 /internal/v1/llm/usage 的请求体。
type usageRequest struct {
	TenantID string `json:"tenant_id"`
	IdemKey  string `json:"idem_key"`
	Status   string `json:"status"`
	Usage    struct {
		Model            string `json:"model"`
		UpstreamModel    string `json:"upstream_model"`
		AgentID          string `json:"agent_id,omitempty"`
		SessionID        string `json:"session_id"`
		TraceID          string `json:"trace_id"`
		Purpose          string `json:"purpose"`
		PromptTokens     int    `json:"prompt_tokens"`
		CompletionTokens int    `json:"completion_tokens"`
		TotalTokens      int    `json:"total_tokens"`
		CostRefMicro     *int64 `json:"cost_ref_micro,omitempty"`
		Stream           bool   `json:"stream"`
	} `json:"usage"`
}

// Record 实现 Recorder：202 成功；5xx/网络错误重试 recordRetries 次；4xx 不重试（幂等重复也是 202）。
func (c *ConsoleClient) Record(ctx context.Context, tenantID string, u Usage, status, idemKey string) error {
	ci := CallInfoFrom(ctx)
	req := usageRequest{TenantID: tenantID, IdemKey: idemKey, Status: status}
	req.Usage.Model, req.Usage.UpstreamModel = u.Model, u.UpstreamModel
	req.Usage.AgentID, req.Usage.SessionID, req.Usage.TraceID, req.Usage.Purpose = ci.AgentID, ci.SessionID, ci.TraceID, ci.Purpose
	req.Usage.PromptTokens, req.Usage.CompletionTokens, req.Usage.TotalTokens = u.PromptTokens, u.CompletionTokens, u.TotalTokens
	req.Usage.CostRefMicro, req.Usage.Stream = u.CostRefMicro, u.Stream

	var lastErr error
	for attempt := 0; attempt <= c.recordRetries; attempt++ {
		st, body, err := c.post(ctx, "/internal/v1/llm/usage", req)
		switch {
		case err == nil && st == http.StatusAccepted:
			return nil
		case err == nil && st >= 400 && st < 500:
			return fmt.Errorf("usage record rejected http %d: %s", st, truncate(body, 200))
		case err != nil:
			lastErr = err
		default:
			lastErr = fmt.Errorf("usage record http %d", st)
		}
		if attempt < c.recordRetries {
			c.sleep(time.Duration(attempt+1) * 200 * time.Millisecond)
		}
	}
	return fmt.Errorf("usage record failed after %d retries: %w", c.recordRetries, lastErr)
}

func (c *ConsoleClient) post(ctx context.Context, path string, body any) (int, []byte, error) {
	buf, err := json.Marshal(body)
	if err != nil {
		return 0, nil, fmt.Errorf("marshal: %w", err)
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, c.baseURL+path, bytes.NewReader(buf))
	if err != nil {
		return 0, nil, err
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Authorization", "Bearer "+c.svcToken)
	resp, err := c.http.Do(req)
	if err != nil {
		return 0, nil, fmt.Errorf("console %s: %w", path, err)
	}
	defer func() { _ = resp.Body.Close() }()
	respBody, _ := io.ReadAll(io.LimitReader(resp.Body, 64<<10))
	return resp.StatusCode, respBody, nil
}

func truncate(b []byte, n int) string {
	if len(b) <= n {
		return string(b)
	}
	return string(b[:n]) + "…"
}

// ErrNoConsole 供调用方在未配置 console 地址时显式失败（不静默无配额）。
var ErrNoConsole = errors.New("llm: console 地址未配置——没有配额门与记账不得出网")
