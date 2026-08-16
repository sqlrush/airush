package svcapi

import (
	"context"
	"errors"
	"net/http"
	"time"

	"github.com/jackc/pgx/v5"

	"github.com/sqlrush/airush/console/internal/repo"
	"github.com/sqlrush/airush/console/internal/tenancy"
	"github.com/sqlrush/airush/libs/apierror"
)

// LLM 配额门与记账的内部 API（spec-1.7 §2.5）。调用方是 agent-runtime 进程里的
// libs/llm.ConsoleClient；DB 访问只在这里——与 gateway→console 同一模式。

// quotaCheckRequest / quotaCheckResponse 与 libs/llm/console.go 的 wire 形态一致。
type quotaCheckRequest struct {
	TenantID string `json:"tenant_id"`
}

type quotaCheckResponse struct {
	Budget          int64 `json:"budget"`
	Used            int64 `json:"used"`
	RemainingTokens int64 `json:"remaining_tokens"`
	HardStop        bool  `json:"hard_stop"`
}

// llmQuotaCheck POST /internal/v1/llm/quota-check
//   - 无配额行 → 200 且 budget=-1（Stage 1 语义：不限）；
//   - hard_stop 且 used >= budget → 429 AR_QUOTA_EXCEEDED；
//   - hard_stop=false 超额 → 200（只由调用方记指标）。
func (s *Server) llmQuotaCheck(w http.ResponseWriter, r *http.Request) error {
	var req quotaCheckRequest
	if err := decodeJSON(r, &req); err != nil {
		return err
	}
	if req.TenantID == "" {
		return apierror.New(apierror.CodeValidationFailed).WithDetails(
			apierror.Detail{Field: "tenant_id", Reason: "必填"})
	}
	if s.store == nil {
		return apierror.New(apierror.CodeCommonNotImplemented)
	}
	ctx := tenancy.WithTenant(r.Context(), req.TenantID)

	var resp quotaCheckResponse
	var exceeded bool
	err := s.store.InTenantTx(ctx, func(ctx context.Context, tx pgx.Tx) error {
		q, err := repo.GetLLMQuota(ctx, tx)
		if errors.Is(err, repo.ErrLLMQuotaNotSet) {
			resp = quotaCheckResponse{Budget: -1, RemainingTokens: -1}
			return nil
		}
		if err != nil {
			return err
		}
		used, err := repo.MonthToDateTokens(ctx, tx, time.Now())
		if err != nil {
			return err
		}
		resp = quotaCheckResponse{
			Budget: q.TokenBudget, Used: used,
			RemainingTokens: max(q.TokenBudget-used, 0), HardStop: q.HardStop,
		}
		exceeded = q.HardStop && used >= q.TokenBudget
		return nil
	})
	if err != nil {
		return err
	}
	if exceeded {
		return apierror.New(apierror.CodeQuotaExceeded)
	}
	return writeJSON(w, http.StatusOK, resp)
}

// usageRequest 与 libs/llm.usageRequest 一致。
type usageRequest struct {
	TenantID string `json:"tenant_id"`
	IdemKey  string `json:"idem_key"`
	Status   string `json:"status"`
	Usage    struct {
		Model            string  `json:"model"`
		UpstreamModel    string  `json:"upstream_model"`
		AgentID          *string `json:"agent_id"`
		SessionID        string  `json:"session_id"`
		TraceID          string  `json:"trace_id"`
		Purpose          string  `json:"purpose"`
		PromptTokens     int     `json:"prompt_tokens"`
		CompletionTokens int     `json:"completion_tokens"`
		TotalTokens      int     `json:"total_tokens"`
		CostRefMicro     *int64  `json:"cost_ref_micro"`
		Stream           bool    `json:"stream"`
	} `json:"usage"`
}

var llmUsageStatuses = map[string]bool{"ok": true, "upstream_error": true, "quota_rejected": true, "aborted": true}

// llmUsage POST /internal/v1/llm/usage → 202；同 idem_key 重复上报也是 202（幂等）。
func (s *Server) llmUsage(w http.ResponseWriter, r *http.Request) error {
	var req usageRequest
	if err := decodeJSON(r, &req); err != nil {
		return err
	}
	switch {
	case req.TenantID == "" || req.IdemKey == "" || req.Usage.Model == "":
		return apierror.New(apierror.CodeValidationFailed).WithDetails(
			apierror.Detail{Field: "tenant_id/idem_key/usage.model", Reason: "必填"})
	case !llmUsageStatuses[req.Status]:
		return apierror.New(apierror.CodeValidationFailed).WithDetails(
			apierror.Detail{Field: "status", Reason: "须为 ok/upstream_error/quota_rejected/aborted"})
	case req.Usage.PromptTokens < 0 || req.Usage.CompletionTokens < 0 || req.Usage.TotalTokens < 0:
		return apierror.New(apierror.CodeValidationFailed).WithDetails(
			apierror.Detail{Field: "usage", Reason: "token 数不得为负"})
	case req.Usage.AgentID != nil && !isUUID(*req.Usage.AgentID):
		return apierror.New(apierror.CodeValidationFailed).WithDetails(
			apierror.Detail{Field: "usage.agent_id", Reason: "必须是 UUID"})
	}
	if s.store == nil {
		return apierror.New(apierror.CodeCommonNotImplemented)
	}
	ctx := tenancy.WithTenant(r.Context(), req.TenantID)
	in := repo.LLMUsageInput{
		IdemKey: req.IdemKey, Model: req.Usage.Model, UpstreamModel: req.Usage.UpstreamModel,
		AgentID: req.Usage.AgentID, SessionID: req.Usage.SessionID, TraceID: req.Usage.TraceID,
		Purpose: req.Usage.Purpose, PromptTokens: req.Usage.PromptTokens,
		CompletionTokens: req.Usage.CompletionTokens, TotalTokens: req.Usage.TotalTokens,
		CostRefMicro: req.Usage.CostRefMicro, Stream: req.Usage.Stream, Status: req.Status,
	}
	err := s.store.InTenantTx(ctx, func(ctx context.Context, tx pgx.Tx) error {
		_, err := repo.InsertLLMUsage(ctx, tx, in)
		return err
	})
	if err != nil {
		return err
	}
	w.WriteHeader(http.StatusAccepted)
	return nil
}
