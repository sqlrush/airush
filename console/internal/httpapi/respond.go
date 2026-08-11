package httpapi

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"regexp"

	"github.com/sqlrush/airush/libs/apierror"
)

const maxBodyBytes = 1 << 20 // 1MB：控制面请求体上限（边界校验，防滥用）

var uuidRe = regexp.MustCompile(`^[0-9a-fA-F]{8}-[0-9a-fA-F]{4}-[0-9a-fA-F]{4}-[0-9a-fA-F]{4}-[0-9a-fA-F]{12}$`)

func isUUID(s string) bool { return uuidRe.MatchString(s) }

// pathUUID 取路径参数并校验 UUID 形态（非法即 400，不触达 DB）。
func pathUUID(r *http.Request, name string) (string, error) {
	v := r.PathValue(name)
	if !isUUID(v) {
		return "", apierror.New(apierror.CodeValidationFailed).WithDetails(
			apierror.Detail{Field: name, Reason: "必须是 UUID"})
	}
	return v, nil
}

// readBody 限额读取请求体（幂等 hash 与解码共用同一份字节）。
func readBody(r *http.Request) ([]byte, error) {
	body, err := io.ReadAll(io.LimitReader(r.Body, maxBodyBytes+1))
	if err != nil {
		return nil, fmt.Errorf("read request body: %w", err)
	}
	if len(body) > maxBodyBytes {
		return nil, apierror.New(apierror.CodeValidationFailed).WithDetails(
			apierror.Detail{Field: "body", Reason: "请求体超过 1MB 上限"})
	}
	return body, nil
}

// decodeStrict 严格解码（未知字段即 400——契约漂移尽早暴露）。
func decodeStrict(body []byte, dst any) error {
	dec := json.NewDecoder(bytes.NewReader(body))
	dec.DisallowUnknownFields()
	if err := dec.Decode(dst); err != nil {
		return apierror.Wrap(apierror.CodeValidationFailed, err).WithDetails(
			apierror.Detail{Field: "body", Reason: "JSON 解析失败或含未知字段"})
	}
	return nil
}

// writeJSON 统一成功响应出口。
func writeJSON(w http.ResponseWriter, status int, v any) error {
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.WriteHeader(status)
	if err := json.NewEncoder(w).Encode(v); err != nil {
		return fmt.Errorf("encode response: %w", err)
	}
	return nil
}

// page 是统一分页响应形态 {items, next_cursor}（development-standards §5）。
type page[T any] struct {
	Items      []T     `json:"items"`
	NextCursor *string `json:"next_cursor"`
}

// newPage 组装分页响应；items 满页时以末行生成游标。
func newPage[T any](items []T, limit int, cursorOf func(T) string) page[T] {
	p := page[T]{Items: items}
	if p.Items == nil {
		p.Items = []T{}
	}
	if len(items) == limit && limit > 0 {
		c := cursorOf(items[len(items)-1])
		p.NextCursor = &c
	}
	return p
}

// oneOf 枚举校验辅助。
func oneOf(v string, allowed ...string) bool {
	for _, a := range allowed {
		if v == a {
			return true
		}
	}
	return false
}

// requireDetails 有校验明细即打包 400。
func requireDetails(details []apierror.Detail) error {
	if len(details) == 0 {
		return nil
	}
	return apierror.New(apierror.CodeValidationFailed).WithDetails(details...)
}
