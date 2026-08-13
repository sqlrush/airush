package accept

import (
	"context"
	"crypto/rand"
	"crypto/subtle"
	"encoding/hex"
	"encoding/json"
	"errors"
	"net/http"
	"strings"
	"time"

	connectorv1 "github.com/sqlrush/airush/proto/gen/go/connector/v1"
)

// collectTimeout 单次 Connector 采集下发-回流的上限。
const collectTimeout = 20 * time.Second

// collectRequest 是内部 collect API 请求（collector → gateway）。
type collectRequest struct {
	ConnectorID  string `json:"connector_id"`
	DatasourceID string `json:"datasource_id"`
	EngineFamily string `json:"engine_family"`
}

// CollectHandler 是 gateway 内部采集 API（spec-1.3 D4）：collector 经它触发向连接器
// 下发 PROBE_METRICS；连接器采集后回 DataUpload 帧，由会话 loop 落 gateway Sink
// （spec-1.3 §2.4 "gateway 转 Sink"）。本端点只回触发终态（成功/错误码），不回传数据。
// svc token 认证（与 console 内部 API 同机制）。
func CollectHandler(servers *Servers, svcToken string) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if subtle.ConstantTimeCompare([]byte(r.Header.Get("Authorization")), []byte("Bearer "+svcToken)) != 1 {
			writeCollectErr(w, http.StatusUnauthorized, "AR_SVC_UNAUTHENTICATED")
			return
		}
		var req collectRequest
		if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 1<<20)).Decode(&req); err != nil ||
			req.ConnectorID == "" || req.DatasourceID == "" {
			writeCollectErr(w, http.StatusBadRequest, "AR_VALIDATION_FAILED")
			return
		}

		payload, _ := json.Marshal(map[string]string{
			"datasource_id": req.DatasourceID, "engine_family": req.EngineFamily,
		})
		cmd := &connectorv1.Command{
			CommandId: newCommandID(),
			Type:      "PROBE_METRICS",
			Payload:   payload,
		}

		ctx, cancel := context.WithTimeout(r.Context(), collectTimeout)
		defer cancel()
		switch err := servers.Dispatch(ctx, req.ConnectorID, cmd); {
		case err == nil:
			w.Header().Set("Content-Type", "application/json; charset=utf-8")
			_ = json.NewEncoder(w).Encode(map[string]string{"status": "ok"})
		case errors.Is(err, ErrConnectorOffline):
			writeCollectErr(w, http.StatusServiceUnavailable, "AR_CONNECTOR_OFFLINE")
		case errors.Is(err, context.DeadlineExceeded), errors.Is(err, context.Canceled):
			writeCollectErr(w, http.StatusGatewayTimeout, "AR_METRICS_COLLECT_FAILED")
		default:
			writeCollectErr(w, http.StatusBadGateway, dispatchErrCode(err))
		}
	})
}

// dispatchErrCode 从连接器回传的错误信号里提取注册码（errors.New(code)），否则归 COLLECT_FAILED。
func dispatchErrCode(err error) string {
	code := err.Error()
	if strings.HasPrefix(code, "AR_") {
		if i := strings.IndexByte(code, ':'); i > 0 {
			return code[:i]
		}
		return code
	}
	return "AR_METRICS_COLLECT_FAILED"
}

func writeCollectErr(w http.ResponseWriter, status int, code string) {
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(map[string]string{"code": code, "message": code})
}

// newCommandID 生成关联用一次性指令 id。
func newCommandID() string {
	b := make([]byte, 12)
	_, _ = rand.Read(b)
	return "cmd_" + hex.EncodeToString(b)
}
