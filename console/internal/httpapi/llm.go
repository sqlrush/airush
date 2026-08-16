package httpapi

import (
	"context"
	"errors"
	"net/http"
	"time"

	"github.com/sqlrush/airush/console/internal/repo"
	"github.com/sqlrush/airush/libs/apierror"
)

// LLM 配额与用量的公开面（spec-1.7 D4）。Stage 1 租户来自默认租户中间件；
// 用量只读、只聚合——单条明细留给 spec-1.15 审计界面。

// llmQuotaView 是 GET /api/v1/llm/quota 的响应：配额 + 本月已用。
type llmQuotaView struct {
	Period          string    `json:"period"`
	TokenBudget     int64     `json:"token_budget"`
	HardStop        bool      `json:"hard_stop"`
	UsedThisMonth   int64     `json:"used_this_month"`
	RemainingTokens int64     `json:"remaining_tokens"`
	UpdatedAt       time.Time `json:"updated_at"`
	Set             bool      `json:"set"` // false = 无配额行（不限）
}

// getLLMQuota GET /api/v1/llm/quota
func (s *Server) getLLMQuota(w http.ResponseWriter, r *http.Request) error {
	var view llmQuotaView
	err := s.store.InTenantTx(r.Context(), func(ctx context.Context, tx repo.Tx) error {
		used, err := repo.MonthToDateTokens(ctx, tx, time.Now())
		if err != nil {
			return err
		}
		q, err := repo.GetLLMQuota(ctx, tx)
		if errors.Is(err, repo.ErrLLMQuotaNotSet) {
			view = llmQuotaView{Period: "monthly", UsedThisMonth: used, RemainingTokens: -1}
			return nil
		}
		if err != nil {
			return err
		}
		view = llmQuotaView{
			Period: q.Period, TokenBudget: q.TokenBudget, HardStop: q.HardStop,
			UsedThisMonth: used, RemainingTokens: max(q.TokenBudget-used, 0), UpdatedAt: q.UpdatedAt, Set: true,
		}
		return nil
	})
	if err != nil {
		return err
	}
	return writeJSON(w, http.StatusOK, view)
}

type putLLMQuotaRequest struct {
	TokenBudget *int64 `json:"token_budget"`
	HardStop    *bool  `json:"hard_stop"`
}

// putLLMQuota PUT /api/v1/llm/quota —— 全量写（两个字段都要给）；预算下调到已用之下即刻生效。
func (s *Server) putLLMQuota(w http.ResponseWriter, r *http.Request) error {
	body, err := readBody(r)
	if err != nil {
		return err
	}
	var req putLLMQuotaRequest
	if err := decodeStrict(body, &req); err != nil {
		return err
	}
	if req.TokenBudget == nil || req.HardStop == nil {
		return apierror.New(apierror.CodeValidationFailed).WithDetails(
			apierror.Detail{Field: "token_budget/hard_stop", Reason: "必填"})
	}
	if *req.TokenBudget < 0 {
		return apierror.New(apierror.CodeValidationFailed).WithDetails(
			apierror.Detail{Field: "token_budget", Reason: "不得为负（0 = 禁用 LLM）"})
	}
	var q repo.LLMQuota
	err = s.store.InTenantTx(r.Context(), func(ctx context.Context, tx repo.Tx) error {
		var err error
		q, err = repo.UpsertLLMQuota(ctx, tx, *req.TokenBudget, *req.HardStop)
		return err
	})
	if err != nil {
		return err
	}
	return writeJSON(w, http.StatusOK, q)
}

// llmUsage GET /api/v1/llm/usage?from=&to=&group_by=day|model
func (s *Server) llmUsage(w http.ResponseWriter, r *http.Request) error {
	q := r.URL.Query()
	from, to, err := parseWindow(q.Get("from"), q.Get("to"))
	if err != nil {
		return err
	}
	groupBy := q.Get("group_by")
	if groupBy == "" {
		groupBy = "day"
	}
	if groupBy != "day" && groupBy != "model" {
		return badParam("group_by", "须为 day 或 model")
	}
	var items []repo.LLMUsageBucket
	err = s.store.InTenantTx(r.Context(), func(ctx context.Context, tx repo.Tx) error {
		var err error
		if groupBy == "day" {
			items, err = repo.LLMUsageByDay(ctx, tx, from, to)
		} else {
			items, err = repo.LLMUsageByModel(ctx, tx, from, to)
		}
		return err
	})
	if err != nil {
		return err
	}
	if items == nil {
		items = []repo.LLMUsageBucket{}
	}
	return writeJSON(w, http.StatusOK, map[string]any{
		"from": from, "to": to, "group_by": groupBy, "items": items,
	})
}
