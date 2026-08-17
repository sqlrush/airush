package runtime

import (
	"bytes"
	"encoding/json"
	"errors"
	"io"
	"net/http"

	"github.com/sqlrush/airush/libs/apierror"
)

// quotaAwareTransport 包在 libs/llm.Meter 外面：Meter 的配额门拒绝是一个 RoundTrip 错误
// （AR_QUOTA_EXCEEDED），对 codexgo 客户端来说是"传输错误"→ 会按网络故障重试 4 次再报错。
// 这里把它改写成一条 HTTP 429 响应（正文带错误码）：客户端与采样层对 429 都不重试，turn 立刻
// 以 error 事件（message 含 AR_QUOTA_EXCEEDED）结束（spec-1.8 §3.5 / T10）。其它错误原样透传。
type quotaAwareTransport struct {
	next http.RoundTripper
}

func (t quotaAwareTransport) RoundTrip(req *http.Request) (*http.Response, error) {
	resp, err := t.next.RoundTrip(req)
	if err == nil {
		return resp, nil
	}
	var ae *apierror.Error
	if !errors.As(err, &ae) || ae.Code != apierror.CodeQuotaExceeded {
		return nil, err
	}
	body, _ := json.Marshal(map[string]any{
		"error": map[string]string{"code": string(ae.Code), "message": apierror.MetaOf(ae.Code).Message, "type": "quota_exceeded"},
	})
	return &http.Response{
		StatusCode:    http.StatusTooManyRequests,
		Status:        "429 Too Many Requests",
		Proto:         "HTTP/1.1",
		ProtoMajor:    1,
		ProtoMinor:    1,
		Header:        http.Header{"Content-Type": []string{"application/json"}},
		Body:          io.NopCloser(bytes.NewReader(body)),
		ContentLength: int64(len(body)),
		Request:       req,
	}, nil
}
