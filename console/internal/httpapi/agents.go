package httpapi

import (
	"context"
	"net/http"

	"github.com/sqlrush/airush/console/internal/repo"
	"github.com/sqlrush/airush/libs/apierror"
)

type agentCreateReq struct {
	Name           string `json:"name"`
	Kind           string `json:"kind"`
	InstructionDoc string `json:"instruction_doc"`
}

func (req *agentCreateReq) validate() []apierror.Detail {
	var d []apierror.Detail
	if req.Name == "" || len(req.Name) > 128 {
		d = append(d, apierror.Detail{Field: "name", Reason: "必填，长度 1..128"})
	}
	if !oneOf(req.Kind, "assistant", "domain") {
		d = append(d, apierror.Detail{Field: "kind", Reason: "必须是 assistant/domain"})
	}
	return d
}

func (s *Server) createAgent(w http.ResponseWriter, r *http.Request) error {
	body, err := readBody(r)
	if err != nil {
		return err
	}
	var req agentCreateReq
	if err := decodeStrict(body, &req); err != nil {
		return err
	}
	if err := requireDetails(req.validate()); err != nil {
		return err
	}
	return s.createWithIdempotency(w, r, body, func(ctx context.Context, tx repo.Tx) (any, error) {
		return repo.InsertAgent(ctx, tx, repo.AgentInput{
			Name: req.Name, Kind: req.Kind, InstructionDoc: req.InstructionDoc,
		})
	})
}

func (s *Server) listAgents(w http.ResponseWriter, r *http.Request) error {
	cursor, limit, err := parsePageParams(r)
	if err != nil {
		return err
	}
	var items []repo.Agent
	err = s.store.InTenantTx(r.Context(), func(ctx context.Context, tx repo.Tx) error {
		items, err = repo.ListAgents(ctx, tx, cursor, limit)
		return err
	})
	if err != nil {
		return err
	}
	return writeJSON(w, http.StatusOK, newPage(items, limit, func(a repo.Agent) string {
		return encodeCursor(a.CreatedAt, a.ID)
	}))
}

func (s *Server) getAgent(w http.ResponseWriter, r *http.Request) error {
	id, err := pathUUID(r, "id")
	if err != nil {
		return err
	}
	var a repo.Agent
	err = s.store.InTenantTx(r.Context(), func(ctx context.Context, tx repo.Tx) error {
		a, err = repo.GetAgent(ctx, tx, id)
		return err
	})
	if err != nil {
		return err
	}
	return writeJSON(w, http.StatusOK, a)
}

type agentPatchReq struct {
	Name           *string `json:"name"`
	Status         *string `json:"status"`
	InstructionDoc *string `json:"instruction_doc"`
}

func (req *agentPatchReq) validate() []apierror.Detail {
	var d []apierror.Detail
	if req.Name != nil && (*req.Name == "" || len(*req.Name) > 128) {
		d = append(d, apierror.Detail{Field: "name", Reason: "长度 1..128"})
	}
	if req.Status != nil && !oneOf(*req.Status, "running", "paused") {
		d = append(d, apierror.Detail{Field: "status", Reason: "必须是 running/paused"})
	}
	return d
}

func (s *Server) patchAgent(w http.ResponseWriter, r *http.Request) error {
	id, err := pathUUID(r, "id")
	if err != nil {
		return err
	}
	body, err := readBody(r)
	if err != nil {
		return err
	}
	var req agentPatchReq
	if err := decodeStrict(body, &req); err != nil {
		return err
	}
	if err := requireDetails(req.validate()); err != nil {
		return err
	}
	var a repo.Agent
	err = s.store.InTenantTx(r.Context(), func(ctx context.Context, tx repo.Tx) error {
		a, err = repo.UpdateAgent(ctx, tx, id, repo.AgentPatch{
			Name: req.Name, Status: req.Status, InstructionDoc: req.InstructionDoc,
		})
		return err
	})
	if err != nil {
		return err
	}
	return writeJSON(w, http.StatusOK, a)
}

func (s *Server) deleteAgent(w http.ResponseWriter, r *http.Request) error {
	id, err := pathUUID(r, "id")
	if err != nil {
		return err
	}
	err = s.store.InTenantTx(r.Context(), func(ctx context.Context, tx repo.Tx) error {
		return repo.DeleteAgent(ctx, tx, id)
	})
	if err != nil {
		return err
	}
	w.WriteHeader(http.StatusNoContent)
	return nil
}
