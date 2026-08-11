package httpapi

import (
	"context"
	"net/http"

	"github.com/sqlrush/airush/console/internal/repo"
	"github.com/sqlrush/airush/libs/apierror"
)

type aliasCreateReq struct {
	Alias  string `json:"alias"`
	Source string `json:"source"`
}

func (req *aliasCreateReq) validate() []apierror.Detail {
	var d []apierror.Detail
	if req.Alias == "" || len(req.Alias) > 64 {
		d = append(d, apierror.Detail{Field: "alias", Reason: "必填，长度 1..64"})
	}
	if req.Source != "" && !oneOf(req.Source, "manual", "conversation") {
		d = append(d, apierror.Detail{Field: "source", Reason: "必须是 manual/conversation"})
	}
	return d
}

func (s *Server) createAlias(w http.ResponseWriter, r *http.Request) error {
	dsID, err := pathUUID(r, "id")
	if err != nil {
		return err
	}
	body, err := readBody(r)
	if err != nil {
		return err
	}
	var req aliasCreateReq
	if err := decodeStrict(body, &req); err != nil {
		return err
	}
	if err := requireDetails(req.validate()); err != nil {
		return err
	}
	source := req.Source
	if source == "" {
		source = "manual"
	}

	return s.createWithIdempotency(w, r, body, func(ctx context.Context, tx repo.Tx) (any, error) {
		// 先取数据源：缺失以 404 语义答复（FK 违规会是 400，语义劣化）
		if _, err := repo.GetDatasource(ctx, tx, dsID); err != nil {
			return nil, err
		}
		return repo.InsertAlias(ctx, tx, dsID, req.Alias, source)
	})
}

func (s *Server) listAliases(w http.ResponseWriter, r *http.Request) error {
	dsID, err := pathUUID(r, "id")
	if err != nil {
		return err
	}
	var items []repo.Alias
	err = s.store.InTenantTx(r.Context(), func(ctx context.Context, tx repo.Tx) error {
		if _, err := repo.GetDatasource(ctx, tx, dsID); err != nil {
			return err
		}
		items, err = repo.ListAliases(ctx, tx, dsID)
		return err
	})
	if err != nil {
		return err
	}
	if items == nil {
		items = []repo.Alias{}
	}
	return writeJSON(w, http.StatusOK, map[string]any{"items": items})
}

func (s *Server) deleteAlias(w http.ResponseWriter, r *http.Request) error {
	dsID, err := pathUUID(r, "id")
	if err != nil {
		return err
	}
	alias := r.PathValue("alias")
	if alias == "" {
		return apierror.New(apierror.CodeValidationFailed).WithDetails(
			apierror.Detail{Field: "alias", Reason: "必填"})
	}
	err = s.store.InTenantTx(r.Context(), func(ctx context.Context, tx repo.Tx) error {
		return repo.DeleteAlias(ctx, tx, dsID, alias)
	})
	if err != nil {
		return err
	}
	w.WriteHeader(http.StatusNoContent)
	return nil
}
