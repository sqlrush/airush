package httpapi

import (
	"context"
	"net/http"

	"github.com/sqlrush/airush/console/internal/repo"
)

// connectors 只读展示面（注册/心跳写路径归 spec-1.2）。

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
