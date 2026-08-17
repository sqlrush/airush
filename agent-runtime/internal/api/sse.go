package api

import (
	"encoding/json"
	"fmt"
	"net/http"
	"strconv"
	"time"

	"github.com/sqlrush/airush/libs/apierror"
)

// jsonRaw 让 []byte 以原样 JSON 而非 base64 输出。
func jsonRaw(b []byte) json.RawMessage {
	if len(b) == 0 {
		return json.RawMessage("null")
	}
	return json.RawMessage(b)
}

// sseKeepalive 是空闲时的注释帧周期（穿透反代/LB 的空闲超时）。
const sseKeepalive = 15 * time.Second

// events：SSE。先回放 seq ≥ from_seq 的持久事件再实时；`id:` = seq（客户端 Last-Event-ID 续订），
// `event:` = 事件类型，`data:` = {seq, turn_id, type, payload, payload_ref}。
func (s *Server) events(w http.ResponseWriter, r *http.Request) error {
	id, err := pathUUID(r, "id")
	if err != nil {
		return err
	}
	from, err := fromSeq(r)
	if err != nil {
		return err
	}
	ch, err := s.core.Events(r.Context(), id, from)
	if err != nil {
		return mapErr(err)
	}
	// ResponseController 穿透中间件包裹（Unwrap）；不支持 Flush 的 writer 才是真错误。
	rc := http.NewResponseController(w)
	flush := func() error { return rc.Flush() }
	h := w.Header()
	h.Set("Content-Type", "text/event-stream")
	h.Set("Cache-Control", "no-cache")
	h.Set("Connection", "keep-alive")
	h.Set("X-Accel-Buffering", "no")
	w.WriteHeader(http.StatusOK)
	if err := flush(); err != nil {
		return apierror.Wrap(apierror.CodeInternalError, err)
	}

	keepalive := time.NewTicker(sseKeepalive)
	defer keepalive.Stop()
	for {
		select {
		case <-r.Context().Done():
			return nil
		case <-keepalive.C:
			if _, err := fmt.Fprint(w, ": keepalive\n\n"); err != nil {
				return nil
			}
			_ = flush()
		case ev, ok := <-ch:
			if !ok {
				return nil
			}
			data, err := json.Marshal(ev)
			if err != nil {
				continue
			}
			if _, err := fmt.Fprintf(w, "id: %d\nevent: %s\ndata: %s\n\n", ev.Seq, ev.Type, data); err != nil {
				return nil
			}
			_ = flush()
		}
	}
}

// fromSeq 取 ?from_seq=（缺省 1）；Last-Event-ID 头优先（浏览器 EventSource 自动续订）。
func fromSeq(r *http.Request) (int64, error) {
	if last := r.Header.Get("Last-Event-ID"); last != "" {
		n, err := strconv.ParseInt(last, 10, 64)
		if err != nil || n < 0 {
			return 0, apierror.New(apierror.CodeValidationFailed).WithDetails(apierror.Detail{Field: "Last-Event-ID", Reason: "必须是非负整数"})
		}
		return n + 1, nil
	}
	raw := r.URL.Query().Get("from_seq")
	if raw == "" {
		return 1, nil
	}
	n, err := strconv.ParseInt(raw, 10, 64)
	if err != nil || n < 0 {
		return 0, apierror.New(apierror.CodeValidationFailed).WithDetails(apierror.Detail{Field: "from_seq", Reason: "必须是非负整数"})
	}
	return n, nil
}
