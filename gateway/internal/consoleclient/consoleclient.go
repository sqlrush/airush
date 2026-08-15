// Package consoleclient 是 gateway 调用 console 内部服务 API 的客户端（spec-1.2 §2.1）。
// gateway 不触碰控制面 schema——注册校验/签发/状态记录全部经此 HTTP 客户端。
package consoleclient

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"time"

	"github.com/sqlrush/airush/libs/metrics"
)

// Client 持有 console 内部 API 基址与 svc token。
type Client struct {
	baseURL  string
	svcToken string
	http     *http.Client
}

// New 构造客户端。
func New(baseURL, svcToken string) *Client {
	return &Client{
		baseURL:  baseURL,
		svcToken: svcToken,
		http:     &http.Client{Timeout: 10 * time.Second},
	}
}

// APIError 携带 console 返回的错误码（供 gateway 分支处理）。
type APIError struct {
	Status int
	Code   string
	Msg    string
}

func (e *APIError) Error() string { return fmt.Sprintf("console %d %s: %s", e.Status, e.Code, e.Msg) }

// EnrollResult 是注册签发结果。
type EnrollResult struct {
	ConnectorID    string `json:"connector_id"`
	CertificatePEM string `json:"certificate_pem"`
	CABundlePEM    string `json:"ca_bundle_pem"`
}

// Enroll 用 enrollment token + CSR 换证书。
func (c *Client) Enroll(ctx context.Context, token, csrPEM, version string) (EnrollResult, error) {
	var out EnrollResult
	err := c.post(ctx, "/internal/v1/connector-enrollments", map[string]string{
		"token": token, "csr_pem": csrPEM, "connector_version": version,
	}, &out)
	return out, err
}

// Handshake 会话建立校验（指纹 + 状态）。
func (c *Client) Handshake(ctx context.Context, tenantID, connectorID, fingerprint string) error {
	return c.post(ctx, "/internal/v1/connector-handshakes", map[string]string{
		"tenant_id": tenantID, "connector_id": connectorID, "fingerprint": fingerprint,
	}, nil)
}

// ReportStatus 记录会话状态迁移（gateway 判定 → console 落库）。
func (c *Client) ReportStatus(ctx context.Context, tenantID, connectorID, status string, heartbeatAt *time.Time) error {
	body := map[string]any{"tenant_id": tenantID, "connector_id": connectorID, "status": status}
	if heartbeatAt != nil {
		body["heartbeat_at"] = heartbeatAt.Format(time.RFC3339Nano)
	}
	return c.post(ctx, "/internal/v1/connector-status", body, nil)
}

func (c *Client) post(ctx context.Context, path string, body, out any) error {
	buf, err := json.Marshal(body)
	if err != nil {
		return fmt.Errorf("consoleclient: marshal: %w", err)
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, c.baseURL+path, bytes.NewReader(buf))
	if err != nil {
		return fmt.Errorf("consoleclient: new request: %w", err)
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Authorization", "Bearer "+c.svcToken)

	resp, err := c.http.Do(req)
	if err != nil {
		return fmt.Errorf("consoleclient: %s: %w", path, err)
	}
	defer func() { _ = resp.Body.Close() }()
	respBody, _ := io.ReadAll(io.LimitReader(resp.Body, 1<<20))

	if resp.StatusCode >= 400 {
		return parseAPIError(resp.StatusCode, respBody)
	}
	if out != nil && len(respBody) > 0 {
		if err := json.Unmarshal(respBody, out); err != nil {
			return fmt.Errorf("consoleclient: decode response: %w", err)
		}
	}
	return nil
}

func parseAPIError(status int, body []byte) error {
	var e struct {
		Code    string `json:"code"`
		Message string `json:"message"`
	}
	_ = json.Unmarshal(body, &e)
	return &APIError{Status: status, Code: e.Code, Msg: e.Message}
}

// UploadMetrics 把 Connector 上报的指标批转发给 console 落库（spec-1.5 D5，§8 Q5-A）。
//
// tenantID 由调用方从 Connector 的 mTLS 证书 SAN 解析后显式传入——不走 context 夹带。
// gateway 是多租户中继，租户是这个操作的一等参数，藏进 ctx 只会让越权更难被看出来。
func (c *Client) UploadMetrics(ctx context.Context, tenantID, connectorID string, batch metrics.Batch) error {
	return c.post(ctx, "/internal/v1/collected/metrics", map[string]any{
		"tenant_id": tenantID, "connector_id": connectorID, "batch": batch,
	}, nil)
}

// UploadSnapshot 把 Connector 上报的快照转发给 console 落库。
func (c *Client) UploadSnapshot(ctx context.Context, tenantID, connectorID string, snap metrics.Snapshot) error {
	return c.post(ctx, "/internal/v1/collected/snapshots", map[string]any{
		"tenant_id": tenantID, "connector_id": connectorID, "snapshot": snap,
	}, nil)
}
