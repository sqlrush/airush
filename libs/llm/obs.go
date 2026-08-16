package llm

import (
	"context"
	"errors"
	"time"

	"go.opentelemetry.io/otel/metric"

	"github.com/sqlrush/airush/libs/apierror"
	"github.com/sqlrush/airush/libs/obs"
)

// spec-1.7 D5：平台侧 LLM 调用观测（spec-0.9 三件套）。label 只用白名单里的 model/status/code——
// model 是逻辑名（chat-default），基数 = 目录条目数，有界；租户/agent 不进 label（进日志）。
var (
	llmRequests = obs.Counter("airush_llm_requests_total", "model", "status", "code")
	llmTokens   = obs.Counter("airush_llm_tokens_total", "model")
	llmLatency  = obs.Histogram("airush_llm_request_duration_ms", "model", "status")
	// 配额门不可达但放行的次数（fail-open 可见性，spec-1.7 R5）。
	llmQuotaCheckFailed = obs.Counter("airush_llm_quota_check_failed_total")
)

func observeCall(ctx context.Context, model string, start time.Time, u Usage, status string, err error) {
	code := ""
	var ae *apierror.Error
	if errors.As(err, &ae) {
		code = string(ae.Code)
	}
	attrs := []string{"model", model, "status", status}
	if code != "" {
		attrs = append(attrs, "code", code)
	}
	llmRequests.Add(ctx, 1, metric.WithAttributes(obs.Labels(attrs...)...))
	if u.TotalTokens > 0 {
		llmTokens.Add(ctx, int64(u.TotalTokens), metric.WithAttributes(obs.Labels("model", model)...))
	}
	llmLatency.Record(ctx, float64(time.Since(start).Microseconds())/1000,
		metric.WithAttributes(obs.Labels("model", model, "status", status)...))
}
