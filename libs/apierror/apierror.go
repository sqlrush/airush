// Package apierror 是统一错误码与 API 错误响应实现（spec-0.8 D2）。
// 码空间 SSOT 在 proto/errors.json（make generate 产出 codes_gen.go）；
// 响应契约 {code, message, trace_id, details}（spec-0.8 §2.2）。
package apierror

import (
	"errors"
	"fmt"
)

// Code 是注册错误码（AR_<DOMAIN>_<REASON>）。
type Code string

// Meta 是码的注册表元数据。
type Meta struct {
	Level      string // E1..E6（spec-0.8 §2.3）
	HTTP       int
	Message    string // 面向用户的模板，禁运行时拼接内部细节
	Deprecated bool
}

// Detail 是 E1 验证类错误的结构化明细（spec-0.8 Q3）。
type Detail struct {
	Field  string `json:"field"`
	Reason string `json:"reason"`
}

// Error 是携带错误码的错误；实现 error 与 Unwrap。
type Error struct {
	Code    Code
	Details []Detail
	wrapped error
}

func (e *Error) Error() string {
	if e.wrapped != nil {
		return fmt.Sprintf("%s: %v", e.Code, e.wrapped)
	}
	return string(e.Code)
}

// Unwrap 支持 errors.Is/As 穿透（development-standards §2）。
func (e *Error) Unwrap() error { return e.wrapped }

// New 构造码错误。
func New(code Code) *Error { return &Error{Code: code} }

// Wrap 在内部错误上附加码（内部细节仅进日志链，不进响应 message）。
func Wrap(code Code, err error) *Error { return &Error{Code: code, wrapped: err} }

// WithDetails 附加结构化明细（仅 E1 验证类使用）。
func (e *Error) WithDetails(details ...Detail) *Error {
	e.Details = append(e.Details, details...)
	return e
}

// MetaOf 返回码元数据；未注册码回退 AR_INTERNAL 元数据（防御）。
func MetaOf(code Code) Meta {
	if m, ok := codeMeta[code]; ok {
		return m
	}
	return codeMeta[CodeInternalError]
}

// FromError 提取错误链上的 *Error；无码错误归一为 AR_INTERNAL（spec-0.8 §3：
// "忘了立码"表现为可见 500 而非泄漏内部错误文本）。
func FromError(err error) (*Error, bool) {
	var ae *Error
	if errors.As(err, &ae) {
		return ae, true
	}
	return Wrap(CodeInternalError, err), false
}
