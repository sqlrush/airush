package repo

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
)

// LLM 配额与用量（spec-1.7 D4）。全部在租户事务内（InTenantTx）：RLS 决定可见性，
// tenant_id 由 tenantExpr 注入，永不接受调用方传入的租户参数。

// LLMQuota 是租户的月度 token 预算。
type LLMQuota struct {
	Period      string    `json:"period"`
	TokenBudget int64     `json:"token_budget"`
	HardStop    bool      `json:"hard_stop"`
	UpdatedAt   time.Time `json:"updated_at"`
}

// LLMUsageInput 是一次调用的记账输入（与 svcapi 请求体一一对应）。
type LLMUsageInput struct {
	IdemKey          string
	Model            string
	UpstreamModel    string
	AgentID          *string
	SessionID        string
	TraceID          string
	Purpose          string
	PromptTokens     int
	CompletionTokens int
	TotalTokens      int
	CostRefMicro     *int64
	Stream           bool
	Status           string
}

// LLMUsageBucket 是按天或按模型聚合的一行。
type LLMUsageBucket struct {
	Key              string `json:"key"` // 日期（YYYY-MM-DD）或模型逻辑名
	Calls            int64  `json:"calls"`
	Failed           int64  `json:"failed"`
	PromptTokens     int64  `json:"prompt_tokens"`
	CompletionTokens int64  `json:"completion_tokens"`
	TotalTokens      int64  `json:"total_tokens"`
}

// ErrLLMQuotaNotSet 表示本租户没有配额行（Stage 1 语义 = 不限；见 spec-1.7 §2.5）。
var ErrLLMQuotaNotSet = errors.New("llm quota not set for tenant")

// GetLLMQuota 取月度配额；无行 → ErrLLMQuotaNotSet。
func GetLLMQuota(ctx context.Context, tx pgx.Tx) (LLMQuota, error) {
	var q LLMQuota
	err := tx.QueryRow(ctx, `SELECT period, token_budget, hard_stop, updated_at
		FROM llm_quotas WHERE period = 'monthly'`).
		Scan(&q.Period, &q.TokenBudget, &q.HardStop, &q.UpdatedAt)
	if errors.Is(err, pgx.ErrNoRows) {
		return LLMQuota{}, ErrLLMQuotaNotSet
	}
	if err != nil {
		return LLMQuota{}, fmt.Errorf("get llm quota: %w", err)
	}
	return q, nil
}

// UpsertLLMQuota 写月度配额（下调到已用之下即刻生效——下一次 quota-check 就会拒）。
func UpsertLLMQuota(ctx context.Context, tx pgx.Tx, tokenBudget int64, hardStop bool) (LLMQuota, error) {
	var q LLMQuota
	err := tx.QueryRow(ctx, `INSERT INTO llm_quotas (tenant_id, period, token_budget, hard_stop, updated_at)
		VALUES (`+tenantExpr+`, 'monthly', $1, $2, now())
		ON CONFLICT (tenant_id, period) DO UPDATE
		SET token_budget = EXCLUDED.token_budget, hard_stop = EXCLUDED.hard_stop, updated_at = now()
		RETURNING period, token_budget, hard_stop, updated_at`, tokenBudget, hardStop).
		Scan(&q.Period, &q.TokenBudget, &q.HardStop, &q.UpdatedAt)
	if err != nil {
		return LLMQuota{}, mapPgError(fmt.Errorf("upsert llm quota: %w", err))
	}
	return q, nil
}

// MonthToDateTokens 返回本自然月（UTC）已用 token 总数。
// 只算 status='ok' 与 'aborted'（后者 token 为 0，但语义上是"发生过的调用"）；
// quota_rejected / upstream_error 没消耗上游 token，不计入。
func MonthToDateTokens(ctx context.Context, tx pgx.Tx, now time.Time) (int64, error) {
	monthStart := time.Date(now.UTC().Year(), now.UTC().Month(), 1, 0, 0, 0, 0, time.UTC)
	var used int64
	err := tx.QueryRow(ctx, `SELECT COALESCE(sum(total_tokens), 0) FROM llm_usage
		WHERE at >= $1 AND status IN ('ok', 'aborted')`, monthStart).Scan(&used)
	if err != nil {
		return 0, fmt.Errorf("month-to-date tokens: %w", err)
	}
	return used, nil
}

// InsertLLMUsage 记一次调用。幂等：同 (tenant, idem_key) 重复插入返回 duplicate=true 且不报错——
// Meter 的记账重试与 console 的 202 语义都建立在这条上。
func InsertLLMUsage(ctx context.Context, tx pgx.Tx, in LLMUsageInput) (duplicate bool, err error) {
	tag, err := tx.Exec(ctx, `INSERT INTO llm_usage
		(tenant_id, idem_key, model, upstream_model, agent_id, session_id, trace_id, purpose,
		 prompt_tokens, completion_tokens, total_tokens, cost_ref_micro, stream, status)
		VALUES (`+tenantExpr+`, $1, $2, $3, $4, $5, $6, $7, $8, $9, $10, $11, $12, $13)
		ON CONFLICT (tenant_id, idem_key) DO NOTHING`,
		in.IdemKey, in.Model, in.UpstreamModel, in.AgentID, in.SessionID, in.TraceID, in.Purpose,
		in.PromptTokens, in.CompletionTokens, in.TotalTokens, in.CostRefMicro, in.Stream, in.Status)
	if err != nil {
		var pgErr *pgconn.PgError
		if errors.As(err, &pgErr) && pgErr.Code == "23514" { // check_violation：非法 status / 负 token
			return false, mapPgError(fmt.Errorf("insert llm usage: %w", err))
		}
		return false, fmt.Errorf("insert llm usage: %w", err)
	}
	return tag.RowsAffected() == 0, nil
}

// LLMUsageByDay / LLMUsageByModel 聚合窗口内用量（窗口护栏在 httpapi）。
func LLMUsageByDay(ctx context.Context, tx pgx.Tx, from, to time.Time) ([]LLMUsageBucket, error) {
	return llmUsageAgg(ctx, tx, `to_char(date_trunc('day', at AT TIME ZONE 'UTC'), 'YYYY-MM-DD')`, from, to)
}

// LLMUsageByModel 按模型逻辑名聚合。
func LLMUsageByModel(ctx context.Context, tx pgx.Tx, from, to time.Time) ([]LLMUsageBucket, error) {
	return llmUsageAgg(ctx, tx, `model`, from, to)
}

// llmUsageAgg 的 keyExpr 是本文件内的两个常量表达式之一，非用户输入。
func llmUsageAgg(ctx context.Context, tx pgx.Tx, keyExpr string, from, to time.Time) ([]LLMUsageBucket, error) {
	// #nosec G201 —— keyExpr 闭集常量
	rows, err := tx.Query(ctx, `SELECT `+keyExpr+` AS k, count(*),
			count(*) FILTER (WHERE status IN ('upstream_error', 'quota_rejected', 'aborted')),
			COALESCE(sum(prompt_tokens), 0), COALESCE(sum(completion_tokens), 0), COALESCE(sum(total_tokens), 0)
		FROM llm_usage WHERE at >= $1 AND at < $2
		GROUP BY k ORDER BY k`, from, to)
	if err != nil {
		return nil, fmt.Errorf("llm usage agg: %w", err)
	}
	defer rows.Close()
	var out []LLMUsageBucket
	for rows.Next() {
		var b LLMUsageBucket
		if err := rows.Scan(&b.Key, &b.Calls, &b.Failed, &b.PromptTokens, &b.CompletionTokens, &b.TotalTokens); err != nil {
			return nil, fmt.Errorf("scan llm usage bucket: %w", err)
		}
		out = append(out, b)
	}
	return out, rows.Err()
}

// EnsureLLMQuota 在无配额行时写入默认预算；已有行不动（运维改过的值优先）。返回是否新建。
func EnsureLLMQuota(ctx context.Context, tx pgx.Tx, tokenBudget int64) (created bool, err error) {
	tag, err := tx.Exec(ctx, `INSERT INTO llm_quotas (tenant_id, period, token_budget, hard_stop)
		VALUES (`+tenantExpr+`, 'monthly', $1, true)
		ON CONFLICT (tenant_id, period) DO NOTHING`, tokenBudget)
	if err != nil {
		return false, mapPgError(fmt.Errorf("ensure llm quota: %w", err))
	}
	return tag.RowsAffected() == 1, nil
}
