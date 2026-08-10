package obs

import (
	"fmt"

	"go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/metric"
)

// allowedLabels 是 metrics label 白名单（spec-0.9 §2.2 定版）：
// 禁入 tenant_id/session_id/instance_id 等无界高基数值；
// 扩充必须修订 spec-0.9 §2.2。
var allowedLabels = map[string]bool{
	"component": true,
	"method":    true,
	"route":     true,
	"code":      true, // spec-0.8 错误码
	"level":     true,
	"skill":     true,
	"model":     true,
	"status":    true, // HTTP 状态类别（2xx/4xx/5xx）
}

// Counter 创建计数器并在构造期校验 label 白名单（fail-fast，spec-0.9 T4）。
func Counter(name string, labels ...string) metric.Int64Counter {
	mustAllowedLabels(name, labels)
	c, err := otel.Meter("airush").Int64Counter(name)
	if err != nil {
		panic(fmt.Sprintf("create counter %s: %v", name, err))
	}
	return c
}

// Histogram 创建耗时直方图（毫秒），构造期同样校验白名单。
func Histogram(name string, labels ...string) metric.Float64Histogram {
	mustAllowedLabels(name, labels)
	h, err := otel.Meter("airush").Float64Histogram(name, metric.WithUnit("ms"))
	if err != nil {
		panic(fmt.Sprintf("create histogram %s: %v", name, err))
	}
	return h
}

// Labels 组装白名单校验过的属性集（记录时刻使用）。
func Labels(kv ...string) []attribute.KeyValue {
	if len(kv)%2 != 0 {
		panic("obs.Labels: 奇数个参数（须 key,value 成对）")
	}
	attrs := make([]attribute.KeyValue, 0, len(kv)/2)
	for i := 0; i < len(kv); i += 2 {
		if !allowedLabels[kv[i]] {
			panic(fmt.Sprintf("obs.Labels: label %q 不在白名单（spec-0.9 §2.2；高基数值禁入 metrics）", kv[i]))
		}
		attrs = append(attrs, attribute.String(kv[i], kv[i+1]))
	}
	return attrs
}

func mustAllowedLabels(name string, labels []string) {
	for _, l := range labels {
		if !allowedLabels[l] {
			panic(fmt.Sprintf("metric %s: label %q 不在白名单（spec-0.9 §2.2）", name, l))
		}
	}
}
