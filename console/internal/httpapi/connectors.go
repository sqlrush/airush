package httpapi

import (
	"context"
	"net/http"
	"time"

	"github.com/sqlrush/airush/console/internal/enrolltoken"
	"github.com/sqlrush/airush/console/internal/repo"
	"github.com/sqlrush/airush/console/internal/tenancy"
	"github.com/sqlrush/airush/libs/apierror"
)

// enrollTokenTTL 注册令牌有效期（spec-1.2 §2.3 定版）。
const enrollTokenTTL = 15 * time.Minute

// connectors：spec-1.1 只读展示面 + spec-1.2 写路径（创建/吊销）。
// 注册令牌明文仅在创建响应出现一次（spec-1.2 §3）。

type connectorCreateReq struct {
	Name     string `json:"name"`
	Location string `json:"location"`
}

func (req *connectorCreateReq) validate() []apierror.Detail {
	var d []apierror.Detail
	if req.Name == "" || len(req.Name) > 128 {
		d = append(d, apierror.Detail{Field: "name", Reason: "必填，长度 1..128"})
	}
	if len(req.Location) > 256 {
		d = append(d, apierror.Detail{Field: "location", Reason: "长度 ≤256"})
	}
	return d
}

// connectorCreateResp 内嵌实体 + 一次性注册令牌（唯一一次明文出现）。
type connectorCreateResp struct {
	repo.Connector
	EnrollmentToken string `json:"enrollment_token"`
}

func (s *Server) createConnector(w http.ResponseWriter, r *http.Request) error {
	body, err := readBody(r)
	if err != nil {
		return err
	}
	var req connectorCreateReq
	if err := decodeStrict(body, &req); err != nil {
		return err
	}
	if err := requireDetails(req.validate()); err != nil {
		return err
	}
	return s.createWithIdempotency(w, r, body, func(ctx context.Context, tx repo.Tx) (any, error) {
		c, err := repo.InsertConnector(ctx, tx, repo.ConnectorInput{
			Name: req.Name, Location: req.Location,
		})
		if err != nil {
			return nil, err
		}
		tenantID, _ := tenancy.FromContext(ctx)
		token, hash, err := enrolltoken.New(tenantID, c.ID)
		if err != nil {
			return nil, apierror.Wrap(apierror.CodeInternalError, err)
		}
		if err := repo.SetEnrollToken(ctx, tx, c.ID, hash, enrollTokenTTL); err != nil {
			return nil, err
		}
		return connectorCreateResp{Connector: c, EnrollmentToken: token}, nil
	})
}

func (s *Server) revokeConnector(w http.ResponseWriter, r *http.Request) error {
	id, err := pathUUID(r, "id")
	if err != nil {
		return err
	}
	err = s.store.InTenantTx(r.Context(), func(ctx context.Context, tx repo.Tx) error {
		return repo.RevokeConnector(ctx, tx, id)
	})
	if err != nil {
		return err
	}
	w.WriteHeader(http.StatusNoContent)
	return nil
}

func (s *Server) listConnectors(w http.ResponseWriter, r *http.Request) error {
	cursor, limit, err := parsePageParams(r)
	if err != nil {
		return err
	}
	var items []repo.Connector
	err = s.store.InTenantTx(r.Context(), func(ctx context.Context, tx repo.Tx) error {
		items, err = repo.ListConnectors(ctx, tx, cursor, limit)
		return err
	})
	if err != nil {
		return err
	}
	return writeJSON(w, http.StatusOK, newPage(items, limit, func(c repo.Connector) string {
		return encodeCursor(c.CreatedAt, c.ID)
	}))
}

func (s *Server) getConnector(w http.ResponseWriter, r *http.Request) error {
	id, err := pathUUID(r, "id")
	if err != nil {
		return err
	}
	var c repo.Connector
	err = s.store.InTenantTx(r.Context(), func(ctx context.Context, tx repo.Tx) error {
		c, err = repo.GetConnector(ctx, tx, id)
		return err
	})
	if err != nil {
		return err
	}
	return writeJSON(w, http.StatusOK, c)
}
