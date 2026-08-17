package api

import (
	"net/http"
	"strconv"

	"github.com/sqlrush/codexgo/pkg/protocol"
	"github.com/sqlrush/codexgo/pkg/threadstore"

	"github.com/sqlrush/airush/agent-runtime/internal/pgstore"
	"github.com/sqlrush/airush/agent-runtime/internal/runtime"
	"github.com/sqlrush/airush/libs/apierror"
	"github.com/sqlrush/airush/libs/tenancy"
)

type createThreadRequest struct {
	TenantID string `json:"tenant_id,omitempty"`
	AgentID  string `json:"agent_id,omitempty"`
	Model    string `json:"model,omitempty"`
	Title    string `json:"title,omitempty"`
}

// checkBodyTenant：body 里的 tenant_id 若给出必须与头一致（两处口径不能打架）。
func checkBodyTenant(r *http.Request, bodyTenant string) error {
	if bodyTenant == "" {
		return nil
	}
	if got, _ := tenancy.FromContext(r.Context()); got != bodyTenant {
		return apierror.New(apierror.CodeValidationFailed).WithDetails(apierror.Detail{Field: "tenant_id", Reason: "与 X-Airush-Tenant 不一致"})
	}
	return nil
}

func (s *Server) createThread(w http.ResponseWriter, r *http.Request) error {
	var req createThreadRequest
	if err := decodeBody(r, &req); err != nil {
		return err
	}
	if err := checkBodyTenant(r, req.TenantID); err != nil {
		return err
	}
	if req.AgentID != "" && !uuidRe.MatchString(req.AgentID) {
		return apierror.New(apierror.CodeValidationFailed).WithDetails(apierror.Detail{Field: "agent_id", Reason: "必须是 UUID"})
	}
	if len(req.Title) > 200 {
		return apierror.New(apierror.CodeValidationFailed).WithDetails(apierror.Detail{Field: "title", Reason: "最长 200 字符"})
	}
	ref, err := s.core.StartThread(r.Context(), runtime.StartThreadInput{AgentID: req.AgentID, Model: req.Model, Title: req.Title})
	if err != nil {
		return mapErr(err)
	}
	return writeJSON(w, http.StatusCreated, map[string]string{"thread_id": ref.ThreadID})
}

type submitTurnRequest struct {
	TenantID string               `json:"tenant_id,omitempty"`
	Input    []protocol.UserInput `json:"input"`
}

type turnResponse struct {
	TurnID string `json:"turn_id,omitempty"`
	Queued bool   `json:"queued"`
}

// submitTurn：接纳为新 turn → 200；steer/排队 → 202 queued=true。
func (s *Server) submitTurn(w http.ResponseWriter, r *http.Request) error {
	id, err := pathUUID(r, "id")
	if err != nil {
		return err
	}
	var req submitTurnRequest
	if err := decodeBody(r, &req); err != nil {
		return err
	}
	if err := checkBodyTenant(r, req.TenantID); err != nil {
		return err
	}
	if len(req.Input) == 0 {
		return apierror.New(apierror.CodeValidationFailed).WithDetails(apierror.Detail{Field: "input", Reason: "至少一项"})
	}
	ref, err := s.core.SubmitTurn(r.Context(), id, runtime.TurnInput{Items: req.Input})
	if err != nil {
		return mapErr(err)
	}
	status := http.StatusOK
	if ref.Queued {
		status = http.StatusAccepted
	}
	return writeJSON(w, status, turnResponse{TurnID: ref.TurnID, Queued: ref.Queued})
}

func (s *Server) interrupt(w http.ResponseWriter, r *http.Request) error {
	id, err := pathUUID(r, "id")
	if err != nil {
		return err
	}
	if err := s.core.Interrupt(r.Context(), id); err != nil {
		return mapErr(err)
	}
	w.WriteHeader(http.StatusNoContent)
	return nil
}

func (s *Server) resume(w http.ResponseWriter, r *http.Request) error {
	id, err := pathUUID(r, "id")
	if err != nil {
		return err
	}
	if err := s.core.ResumeThread(r.Context(), id); err != nil {
		return mapErr(err)
	}
	w.WriteHeader(http.StatusNoContent)
	return nil
}

// threadView 是线程的对外投影（不含租户列）。
type threadView struct {
	ThreadID  string  `json:"thread_id"`
	AgentID   *string `json:"agent_id,omitempty"`
	Title     string  `json:"title"`
	Status    string  `json:"status"`
	Model     string  `json:"model"`
	LastSeq   int64   `json:"last_seq"`
	CreatedAt string  `json:"created_at"`
	UpdatedAt string  `json:"updated_at"`
}

func viewOf(info pgstore.ThreadInfo) threadView {
	return threadView{
		ThreadID: info.ID, AgentID: info.AgentID, Title: info.Title, Status: string(info.Status), Model: info.Model,
		LastSeq: info.LastSeq, CreatedAt: info.CreatedAt.UTC().Format(timeLayout), UpdatedAt: info.UpdatedAt.UTC().Format(timeLayout),
	}
}

const timeLayout = "2006-01-02T15:04:05.000Z07:00"

func (s *Server) getThread(w http.ResponseWriter, r *http.Request) error {
	id, err := pathUUID(r, "id")
	if err != nil {
		return err
	}
	if s.engine == nil {
		return apierror.New(apierror.CodeInternalError)
	}
	info, err := s.engine.Store().GetThreadInfo(r.Context(), protocol.NewThreadID(id))
	if err != nil {
		return mapErr(err)
	}
	return writeJSON(w, http.StatusOK, viewOf(info))
}

// listThreads：keyset 分页（cursor 不透明）；archived=true 列归档。
func (s *Server) listThreads(w http.ResponseWriter, r *http.Request) error {
	if s.engine == nil {
		return apierror.New(apierror.CodeInternalError)
	}
	limit, err := limitParam(r, 25, 200)
	if err != nil {
		return err
	}
	var cursor *string
	if c := r.URL.Query().Get("cursor"); c != "" {
		cursor = &c
	}
	page, err := s.engine.Store().Threads().ListThreads(r.Context(), threadstore.ListThreadsParams{
		PageSize: limit, Cursor: cursor, Archived: r.URL.Query().Get("archived") == "true",
	})
	if err != nil {
		return mapErr(err)
	}
	items := make([]map[string]any, 0, len(page.Items))
	for _, th := range page.Items {
		item := map[string]any{
			"thread_id":  th.ThreadID.String(),
			"title":      derefStr(th.Name),
			"model":      derefStr(th.Model),
			"created_at": th.CreatedAt.UTC().Format(timeLayout),
			"updated_at": th.UpdatedAt.UTC().Format(timeLayout),
		}
		if th.ParentThreadID != nil {
			item["parent_thread_id"] = th.ParentThreadID.String()
		}
		items = append(items, item)
	}
	return writeJSON(w, http.StatusOK, map[string]any{"items": items, "next_cursor": page.NextCursor})
}

// listItems：0.147 thread/items/list 语义（seq keyset）。
func (s *Server) listItems(w http.ResponseWriter, r *http.Request) error {
	id, err := pathUUID(r, "id")
	if err != nil {
		return err
	}
	if s.engine == nil {
		return apierror.New(apierror.CodeInternalError)
	}
	limit, err := limitParam(r, 50, 200)
	if err != nil {
		return err
	}
	var cursor *string
	if c := r.URL.Query().Get("cursor"); c != "" {
		cursor = &c
	}
	var turnID *string
	if t := r.URL.Query().Get("turn_id"); t != "" {
		turnID = &t
	}
	page, err := s.engine.Store().Threads().ListItems(r.Context(), threadstore.ListItemsParams{
		ThreadID: protocol.NewThreadID(id), Cursor: cursor, PageSize: limit, TurnID: turnID, IncludeArchived: true,
	})
	if err != nil {
		return mapErr(err)
	}
	items := make([]map[string]any, 0, len(page.Items))
	for _, it := range page.Items {
		items = append(items, map[string]any{
			"item_id": it.ItemID, "turn_id": it.TurnID, "seq": it.UpdatedAtOrdinal,
			"created_at_ms": it.CreatedAtMS, "item": jsonRaw(it.ItemJSON),
		})
	}
	return writeJSON(w, http.StatusOK, map[string]any{"items": items, "next_cursor": page.NextCursor})
}

func (s *Server) deleteThread(w http.ResponseWriter, r *http.Request) error {
	id, err := pathUUID(r, "id")
	if err != nil {
		return err
	}
	if s.engine == nil {
		return apierror.New(apierror.CodeInternalError)
	}
	err = s.engine.Store().Threads().DeleteThread(r.Context(), threadstore.DeleteThreadParams{ThreadID: protocol.NewThreadID(id)})
	if err != nil {
		return mapErr(err)
	}
	w.WriteHeader(http.StatusNoContent)
	return nil
}

func limitParam(r *http.Request, def, max int) (int, error) {
	raw := r.URL.Query().Get("limit")
	if raw == "" {
		return def, nil
	}
	n, err := strconv.Atoi(raw)
	if err != nil || n <= 0 || n > max {
		return 0, apierror.New(apierror.CodeValidationFailed).WithDetails(apierror.Detail{Field: "limit", Reason: "1..200"})
	}
	return n, nil
}

func derefStr(p *string) string {
	if p == nil {
		return ""
	}
	return *p
}
