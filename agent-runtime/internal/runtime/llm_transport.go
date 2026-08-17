package runtime

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"

	"github.com/sqlrush/airush/libs/apierror"
)

// quotaAwareTransport 包在 libs/llm.Meter 外面，把 Meter 映射出的错误还原成 codexgo 客户端能正确
// 分类的 HTTP 响应（spec-1.8 T10 / T18）：
//   - 配额门拒绝（AR_QUOTA_EXCEEDED）→ 429：客户端与采样层对 429 都不重试，turn 立刻以 error 事件结束；
//   - 网关 4xx（Meter 映射为 AR_UPSTREAM_LLM_FAILED "gateway http 4xx" / MODEL_UNKNOWN）→ 同状态码的
//     响应：4xx 是请求问题，按"网络错误"重试 4 次只是拖慢失败；正文带平台错误码（上游正文仍只在
//     Meter 日志，AD-3）；
//   - 其余（5xx、超时、传输错误）原样透传——那些本就该重试。
type quotaAwareTransport struct {
	next http.RoundTripper
}

func (t quotaAwareTransport) RoundTrip(req *http.Request) (*http.Response, error) {
	resp, err := t.next.RoundTrip(req)
	if err == nil {
		return resp, nil
	}
	var ae *apierror.Error
	if !errors.As(err, &ae) {
		return nil, err
	}
	status, ok := gatewayStatusFor(ae)
	if !ok {
		return nil, err
	}
	return syntheticResponse(req, status, ae), nil
}

// gatewayStatusFor 判定该错误是否应作为不可重试的 HTTP 状态回给客户端。
func gatewayStatusFor(ae *apierror.Error) (int, bool) {
	switch ae.Code {
	case apierror.CodeQuotaExceeded:
		return http.StatusTooManyRequests, true
	case apierror.CodeUpstreamLlmModelUnknown:
		return http.StatusBadRequest, true
	case apierror.CodeUpstreamLlmFailed:
		var status int
		if cause := errors.Unwrap(ae); cause != nil {
			if _, err := fmt.Sscanf(cause.Error(), "gateway http %d", &status); err == nil && status >= 400 && status < 500 {
				return status, true
			}
		}
	}
	return 0, false
}

func syntheticResponse(req *http.Request, status int, ae *apierror.Error) *http.Response {
	body, _ := json.Marshal(map[string]any{
		"error": map[string]string{"code": string(ae.Code), "message": apierror.MetaOf(ae.Code).Message},
	})
	return &http.Response{
		StatusCode:    status,
		Status:        fmt.Sprintf("%d %s", status, http.StatusText(status)),
		Proto:         "HTTP/1.1",
		ProtoMajor:    1,
		ProtoMinor:    1,
		Header:        http.Header{"Content-Type": []string{"application/json"}},
		Body:          io.NopCloser(bytes.NewReader(body)),
		ContentLength: int64(len(body)),
		Request:       req,
	}
}
