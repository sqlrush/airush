package collector

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"time"
)

// ConnectorCollector 触发 Connector 通道的一次采集：经 gateway 内部 collect API 下发
// PROBE_METRICS 到目标连接器；连接器采集后回 DataUpload，由 gateway 落其 Sink
// （spec-1.3 §2.4）。本接口只回触发终态（成功/失败），不承载 batch。Direct 通道不经此路径。
type ConnectorCollector interface {
	TriggerCollect(ctx context.Context, connectorID, datasourceID, engineFamily string) error
}

// GatewayClient 调用 gateway 内部采集 API（svc token 认证，与 console↔gateway 同信任域）。
type GatewayClient struct {
	baseURL  string
	svcToken string
	http     *http.Client
}

// NewGatewayClient 构造（baseURL 形如 http://airush-gateway:8081，无尾斜杠）。
func NewGatewayClient(baseURL, svcToken string) *GatewayClient {
	return &GatewayClient{
		baseURL:  baseURL,
		svcToken: svcToken,
		http:     &http.Client{Timeout: 25 * time.Second},
	}
}

var _ ConnectorCollector = (*GatewayClient)(nil)

// collectRequest 与 gateway 侧 collect API 契约一致。
type collectRequest struct {
	ConnectorID  string `json:"connector_id"`
	DatasourceID string `json:"datasource_id"`
	EngineFamily string `json:"engine_family"`
}

// TriggerCollect 触发一次 Connector 采集（数据由 gateway 落其 Sink，本调用只判成败）。
func (c *GatewayClient) TriggerCollect(ctx context.Context, connectorID, datasourceID, engineFamily string) error {
	body, err := json.Marshal(collectRequest{
		ConnectorID: connectorID, DatasourceID: datasourceID, EngineFamily: engineFamily,
	})
	if err != nil {
		return fmt.Errorf("collector: marshal collect request: %w", err)
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, c.baseURL+"/internal/v1/collect", bytes.NewReader(body))
	if err != nil {
		return fmt.Errorf("collector: new request: %w", err)
	}
	req.Header.Set("Authorization", "Bearer "+c.svcToken)
	req.Header.Set("Content-Type", "application/json")

	resp, err := c.http.Do(req)
	if err != nil {
		return fmt.Errorf("collector: gateway collect: %w", err)
	}
	defer func() { _ = resp.Body.Close() }()

	payload, err := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	if err != nil {
		return fmt.Errorf("collector: read collect response: %w", err)
	}
	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("collector: gateway collect status %d: %s", resp.StatusCode, gatewayErrCode(payload))
	}
	return nil
}

// gatewayErrCode 从 gateway 错误响应体尽力取出 code（失败则原样返回截断文本）。
func gatewayErrCode(payload []byte) string {
	var e struct {
		Code string `json:"code"`
	}
	if json.Unmarshal(payload, &e) == nil && e.Code != "" {
		return e.Code
	}
	if len(payload) > 200 {
		payload = payload[:200]
	}
	return string(payload)
}
