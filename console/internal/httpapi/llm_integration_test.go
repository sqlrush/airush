//go:build integration

package httpapi

import (
	"encoding/json"
	"fmt"
	"testing"
	"time"
)

// TestLLMQuotaAndUsageAPI spec-1.7 T18（公开面部分）：配额 GET/PUT、本月已用、用量按 day/model 聚合、
// 跨租户不可见、窗口护栏。内部面（quota-check/usage 记账）在 svcapi 的集成用例里。
func TestLLMQuotaAndUsageAPI(t *testing.T) {
	env := newAPIEnv(t)

	t.Run("dev 租户有 seed 配额，B 租户无配额=不限", func(t *testing.T) {
		status, body := env.do(t, env.dev, "GET", "/api/v1/llm/quota", nil, nil)
		if status != 200 {
			t.Fatalf("GET quota: %d %.200s", status, body)
		}
		m := jsonMap(t, body)
		if m["set"] != true || m["token_budget"] != float64(50_000_000) || m["hard_stop"] != true {
			t.Fatalf("dev quota = %v", m)
		}
		status, body = env.do(t, env.b, "GET", "/api/v1/llm/quota", nil, nil)
		if status != 200 || jsonMap(t, body)["set"] != false {
			t.Fatalf("B quota: %d %.200s（应为未设置=不限，且看不到 dev 的行）", status, body)
		}
	})

	t.Run("PUT 校验与写入", func(t *testing.T) {
		status, body := env.do(t, env.dev, "PUT", "/api/v1/llm/quota", map[string]any{"token_budget": -1, "hard_stop": true}, nil)
		wantCode(t, status, body, 400, "AR_VALIDATION_FAILED")
		status, body = env.do(t, env.dev, "PUT", "/api/v1/llm/quota", map[string]any{"token_budget": 1000}, nil)
		wantCode(t, status, body, 400, "AR_VALIDATION_FAILED") // 缺 hard_stop
		status, body = env.do(t, env.dev, "PUT", "/api/v1/llm/quota", map[string]any{"token_budget": 1000, "hard_stop": false, "bogus": 1}, nil)
		wantCode(t, status, body, 400, "AR_VALIDATION_FAILED") // 未知字段

		status, body = env.do(t, env.dev, "PUT", "/api/v1/llm/quota", map[string]any{"token_budget": 1000, "hard_stop": false}, nil)
		if status != 200 {
			t.Fatalf("PUT quota: %d %.200s", status, body)
		}
		status, body = env.do(t, env.dev, "GET", "/api/v1/llm/quota", nil, nil)
		m := jsonMap(t, body)
		if m["token_budget"] != float64(1000) || m["hard_stop"] != false {
			t.Fatalf("回读 = %v", m)
		}
		// B 租户不受影响
		if _, b := env.do(t, env.b, "GET", "/api/v1/llm/quota", nil, nil); jsonMap(t, b)["set"] != false {
			t.Fatalf("B 租户被 dev 的 PUT 影响: %.200s", b)
		}
	})

	t.Run("用量聚合按 day/model 且跨租户不可见", func(t *testing.T) {
		// 直接以超级用户造两租户各几行（写路径在 svcapi 用例验）
		now := time.Now().UTC()
		for i, row := range []struct {
			tenant, model, status string
			tokens                int
			at                    time.Time
		}{
			{devTenantID, "chat-default", "ok", 100, now},
			{devTenantID, "chat-default", "ok", 50, now.Add(-time.Hour)},
			{devTenantID, "chat-strong", "upstream_error", 0, now},
			{devTenantID, "chat-default", "ok", 7, now.Add(-48 * time.Hour)},
			{tenantBID, "chat-default", "ok", 9999, now},
		} {
			if _, err := env.admin.Exec(`INSERT INTO llm_usage (tenant_id, model, prompt_tokens, completion_tokens,
				total_tokens, status, idem_key, at) VALUES ($1, $2, $3, 0, $3, $4, $5, $6)`,
				row.tenant, row.model, row.tokens, row.status, fmt.Sprintf("k-%d", i), row.at); err != nil {
				t.Fatalf("seed usage: %v", err)
			}
		}
		from := now.Add(-2 * time.Hour).Format(time.RFC3339)
		to := now.Add(time.Minute).Format(time.RFC3339)

		status, body := env.do(t, env.dev, "GET", "/api/v1/llm/usage?group_by=model&from="+from+"&to="+to, nil, nil)
		if status != 200 {
			t.Fatalf("usage by model: %d %.200s", status, body)
		}
		var byModel struct {
			Items []struct {
				Key         string `json:"key"`
				Calls       int64  `json:"calls"`
				Failed      int64  `json:"failed"`
				TotalTokens int64  `json:"total_tokens"`
			} `json:"items"`
		}
		mustJSON(t, body, &byModel)
		if len(byModel.Items) != 2 {
			t.Fatalf("by model items = %+v, want 2（chat-default/chat-strong；不含 48h 前那行与 B 租户）", byModel.Items)
		}
		for _, it := range byModel.Items {
			switch it.Key {
			case "chat-default":
				if it.Calls != 2 || it.TotalTokens != 150 || it.Failed != 0 {
					t.Fatalf("chat-default = %+v", it)
				}
			case "chat-strong":
				if it.Calls != 1 || it.Failed != 1 {
					t.Fatalf("chat-strong = %+v", it)
				}
			default:
				t.Fatalf("unexpected key %q", it.Key)
			}
		}

		status, body = env.do(t, env.dev, "GET", "/api/v1/llm/usage?group_by=day&from="+from+"&to="+to, nil, nil)
		if status != 200 {
			t.Fatalf("usage by day: %d %.200s", status, body)
		}
		// 本月已用（quota 视图）= 150 + 0 + 7 = 157（48h 前那行若跨月则不计——用绝对断言前判月份）
		status, body = env.do(t, env.dev, "GET", "/api/v1/llm/quota", nil, nil)
		used := jsonMap(t, body)["used_this_month"].(float64)
		want := float64(150)
		if now.Add(-48*time.Hour).Month() == now.Month() {
			want += 7
		}
		if used != want {
			t.Fatalf("used_this_month = %v, want %v（quota_rejected/upstream_error 不计，B 租户不计）", used, want)
		}

		// 护栏：group_by 非法、窗口越界
		status, body = env.do(t, env.dev, "GET", "/api/v1/llm/usage?group_by=hour", nil, nil)
		wantCode(t, status, body, 400, "AR_VALIDATION_FAILED")
		status, body = env.do(t, env.dev, "GET", "/api/v1/llm/usage?from=2020-01-01T00:00:00Z", nil, nil)
		wantCode(t, status, body, 400, "AR_VALIDATION_FAILED")
	})
}

func mustJSON(t *testing.T, b []byte, dst any) {
	t.Helper()
	if err := json.Unmarshal(b, dst); err != nil {
		t.Fatalf("decode %.200s: %v", b, err)
	}
}
