package httpapi

import (
	"context"
	"net/http"

	"github.com/sqlrush/airush/console/internal/repo"
	"github.com/sqlrush/airush/libs/apierror"
)

type groupCreateReq struct {
	Name string `json:"name"`
	Kind string `json:"kind"`
}

func (req *groupCreateReq) validate() []apierror.Detail {
	var d []apierror.Detail
	if req.Name == "" || len(req.Name) > 128 {
		d = append(d, apierror.Detail{Field: "name", Reason: "必填，长度 1..128"})
	}
	if !oneOf(req.Kind, "primary_standby", "cluster") {
		d = append(d, apierror.Detail{Field: "kind", Reason: "必须是 primary_standby/cluster"})
	}
	return d
}

func (s *Server) createGroup(w http.ResponseWriter, r *http.Request) error {
	body, err := readBody(r)
	if err != nil {
		return err
	}
	var req groupCreateReq
	if err := decodeStrict(body, &req); err != nil {
		return err
	}
	if err := requireDetails(req.validate()); err != nil {
		return err
	}
	return s.createWithIdempotency(w, r, body, func(ctx context.Context, tx repo.Tx) (any, error) {
		return repo.InsertGroup(ctx, tx, repo.GroupInput{Name: req.Name, Kind: req.Kind})
	})
}

func (s *Server) listGroups(w http.ResponseWriter, r *http.Request) error {
	cursor, limit, err := parsePageParams(r)
	if err != nil {
		return err
	}
	var items []repo.Group
	err = s.store.InTenantTx(r.Context(), func(ctx context.Context, tx repo.Tx) error {
		items, err = repo.ListGroups(ctx, tx, cursor, limit)
		return err
	})
	if err != nil {
		return err
	}
	return writeJSON(w, http.StatusOK, newPage(items, limit, func(g repo.Group) string {
		return encodeCursor(g.CreatedAt, g.ID)
	}))
}

func (s *Server) getGroup(w http.ResponseWriter, r *http.Request) error {
	id, err := pathUUID(r, "id")
	if err != nil {
		return err
	}
	var g repo.Group
	err = s.store.InTenantTx(r.Context(), func(ctx context.Context, tx repo.Tx) error {
		g, err = repo.GetGroup(ctx, tx, id)
		return err
	})
	if err != nil {
		return err
	}
	return writeJSON(w, http.StatusOK, g)
}

type groupPatchReq struct {
	Name *string `json:"name"`
}

func (s *Server) patchGroup(w http.ResponseWriter, r *http.Request) error {
	id, err := pathUUID(r, "id")
	if err != nil {
		return err
	}
	body, err := readBody(r)
	if err != nil {
		return err
	}
	var req groupPatchReq
	if err := decodeStrict(body, &req); err != nil {
		return err
	}
	if req.Name == nil || *req.Name == "" || len(*req.Name) > 128 {
		return apierror.New(apierror.CodeValidationFailed).WithDetails(
			apierror.Detail{Field: "name", Reason: "必填，长度 1..128（kind 不可变，改 kind = 重建组）"})
	}
	var g repo.Group
	err = s.store.InTenantTx(r.Context(), func(ctx context.Context, tx repo.Tx) error {
		g, err = repo.RenameGroup(ctx, tx, id, *req.Name)
		return err
	})
	if err != nil {
		return err
	}
	return writeJSON(w, http.StatusOK, g)
}

func (s *Server) deleteGroup(w http.ResponseWriter, r *http.Request) error {
	id, err := pathUUID(r, "id")
	if err != nil {
		return err
	}
	err = s.store.InTenantTx(r.Context(), func(ctx context.Context, tx repo.Tx) error {
		return repo.DeleteGroup(ctx, tx, id)
	})
	if err != nil {
		return err
	}
	w.WriteHeader(http.StatusNoContent)
	return nil
}
