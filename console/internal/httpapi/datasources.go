package httpapi

import (
	"context"
	"net/http"

	"github.com/sqlrush/airush/console/internal/repo"
	"github.com/sqlrush/airush/libs/apierror"
)

// datasourceCreateReq 创建请求；直连模式凭据内联（明文只活在本请求解析→加密栈帧，
// 禁日志/禁回显，spec-1.1 §3）。
type datasourceCreateReq struct {
	Name          string         `json:"name"`
	EngineFamily  string         `json:"engine_family"`
	Engine        string         `json:"engine"`
	EngineVersion string         `json:"engine_version"`
	ConnectMode   string         `json:"connect_mode"`
	ConnectorID   string         `json:"connector_id"`
	Host          string         `json:"host"`
	Port          int            `json:"port"`
	DatabaseName  string         `json:"database_name"`
	GroupID       string         `json:"group_id"`
	GroupRole     string         `json:"group_role"`
	AgentID       string         `json:"agent_id"`
	Credential    *credentialReq `json:"credential"`
}

type credentialReq struct {
	Username string `json:"username"`
	Password string `json:"password"`
}

func (req *datasourceCreateReq) validate() []apierror.Detail {
	var d []apierror.Detail
	if req.Name == "" || len(req.Name) > 128 {
		d = append(d, apierror.Detail{Field: "name", Reason: "必填，长度 1..128"})
	}
	if !oneOf(req.EngineFamily, "postgres", "mysql", "dm") {
		d = append(d, apierror.Detail{Field: "engine_family", Reason: "必须是 postgres/mysql/dm"})
	}
	if req.Host == "" {
		d = append(d, apierror.Detail{Field: "host", Reason: "必填"})
	}
	if req.Port < 1 || req.Port > 65535 {
		d = append(d, apierror.Detail{Field: "port", Reason: "必须是 1..65535"})
	}
	if (req.GroupID == "") != (req.GroupRole == "") {
		d = append(d, apierror.Detail{Field: "group_id", Reason: "group_id 与 group_role 必须成对出现"})
	}
	if req.GroupRole != "" && !oneOf(req.GroupRole, "primary", "standby", "replica", "node") {
		d = append(d, apierror.Detail{Field: "group_role", Reason: "必须是 primary/standby/replica/node"})
	}
	return append(d, req.validateMode()...)
}

// validateMode 双模式形态校验（API 层先答复 400，DB CHECK 兜底同语义）。
func (req *datasourceCreateReq) validateMode() []apierror.Detail {
	switch req.ConnectMode {
	case "connector":
		if req.ConnectorID == "" || !isUUID(req.ConnectorID) || req.Credential != nil {
			return []apierror.Detail{{
				Field:  "connect_mode",
				Reason: "connector 模式必须携带 connector_id（UUID）且不得内联凭据",
			}}
		}
	case "direct":
		if req.Credential == nil || req.Credential.Username == "" ||
			req.Credential.Password == "" || req.ConnectorID != "" {
			return []apierror.Detail{{
				Field:  "connect_mode",
				Reason: "direct 模式必须内联 credential{username,password} 且不得携带 connector_id",
			}}
		}
	default:
		return []apierror.Detail{{Field: "connect_mode", Reason: "必须是 connector/direct"}}
	}
	return nil
}

func (s *Server) createDatasource(w http.ResponseWriter, r *http.Request) error {
	body, err := readBody(r)
	if err != nil {
		return err
	}
	var req datasourceCreateReq
	if err := decodeStrict(body, &req); err != nil {
		return err
	}
	if details := req.validate(); len(details) > 0 {
		// 模式形态错误专用码（spec-1.1 §2.3）；其余走通用校验码
		if details[0].Field == "connect_mode" {
			return apierror.New(apierror.CodeDatasourceModeMismatch).WithDetails(details...)
		}
		return requireDetails(details)
	}

	return s.createWithIdempotency(w, r, body, func(ctx context.Context, tx repo.Tx) (any, error) {
		in := repo.DatasourceInput{
			Name: req.Name, EngineFamily: req.EngineFamily, Engine: req.Engine,
			EngineVersion: req.EngineVersion, ConnectMode: req.ConnectMode,
			ConnectorID: nilIfEmpty(req.ConnectorID), Host: req.Host, Port: req.Port,
			DatabaseName: req.DatabaseName, GroupID: nilIfEmpty(req.GroupID),
			GroupRole: nilIfEmpty(req.GroupRole), AgentID: nilIfEmpty(req.AgentID),
		}
		if req.ConnectMode == "direct" {
			ciphertext, err := s.sealer.Seal([]byte(req.Credential.Password))
			if err != nil {
				return nil, apierror.Wrap(apierror.CodeInternalError, err)
			}
			credID, err := repo.InsertCredential(ctx, tx, req.Credential.Username, ciphertext, s.sealer.KeyID())
			if err != nil {
				return nil, err
			}
			in.CredentialID = &credID
		}
		return repo.InsertDatasource(ctx, tx, in)
	})
}

func (s *Server) listDatasources(w http.ResponseWriter, r *http.Request) error {
	cursor, limit, err := parsePageParams(r)
	if err != nil {
		return err
	}
	var items []repo.Datasource
	err = s.store.InTenantTx(r.Context(), func(ctx context.Context, tx repo.Tx) error {
		items, err = repo.ListDatasources(ctx, tx, cursor, limit)
		return err
	})
	if err != nil {
		return err
	}
	return writeJSON(w, http.StatusOK, newPage(items, limit, func(d repo.Datasource) string {
		return encodeCursor(d.CreatedAt, d.ID)
	}))
}

func (s *Server) getDatasource(w http.ResponseWriter, r *http.Request) error {
	id, err := pathUUID(r, "id")
	if err != nil {
		return err
	}
	var ds repo.Datasource
	err = s.store.InTenantTx(r.Context(), func(ctx context.Context, tx repo.Tx) error {
		ds, err = repo.GetDatasource(ctx, tx, id)
		return err
	})
	if err != nil {
		return err
	}
	return writeJSON(w, http.StatusOK, ds)
}

// datasourcePatchReq 部分更新；group_id/agent_id 空串 = 解绑。
type datasourcePatchReq struct {
	Name          *string `json:"name"`
	Engine        *string `json:"engine"`
	EngineVersion *string `json:"engine_version"`
	Host          *string `json:"host"`
	Port          *int    `json:"port"`
	DatabaseName  *string `json:"database_name"`
	GroupID       *string `json:"group_id"`
	GroupRole     *string `json:"group_role"`
	AgentID       *string `json:"agent_id"`
}

func (req *datasourcePatchReq) validate() []apierror.Detail {
	var d []apierror.Detail
	if req.Name != nil && (*req.Name == "" || len(*req.Name) > 128) {
		d = append(d, apierror.Detail{Field: "name", Reason: "长度 1..128"})
	}
	if req.Host != nil && *req.Host == "" {
		d = append(d, apierror.Detail{Field: "host", Reason: "不得为空"})
	}
	if req.Port != nil && (*req.Port < 1 || *req.Port > 65535) {
		d = append(d, apierror.Detail{Field: "port", Reason: "必须是 1..65535"})
	}
	if req.GroupRole != nil && *req.GroupRole != "" &&
		!oneOf(*req.GroupRole, "primary", "standby", "replica", "node") {
		d = append(d, apierror.Detail{Field: "group_role", Reason: "必须是 primary/standby/replica/node"})
	}
	return d
}

func (s *Server) patchDatasource(w http.ResponseWriter, r *http.Request) error {
	id, err := pathUUID(r, "id")
	if err != nil {
		return err
	}
	body, err := readBody(r)
	if err != nil {
		return err
	}
	var req datasourcePatchReq
	if err := decodeStrict(body, &req); err != nil {
		return err
	}
	if err := requireDetails(req.validate()); err != nil {
		return err
	}

	var ds repo.Datasource
	err = s.store.InTenantTx(r.Context(), func(ctx context.Context, tx repo.Tx) error {
		ds, err = repo.UpdateDatasource(ctx, tx, id, repo.DatasourcePatch{
			Name: req.Name, Engine: req.Engine, EngineVersion: req.EngineVersion,
			Host: req.Host, Port: req.Port, DatabaseName: req.DatabaseName,
			GroupID: req.GroupID, GroupRole: req.GroupRole, AgentID: req.AgentID,
		})
		return err
	})
	if err != nil {
		return err
	}
	return writeJSON(w, http.StatusOK, ds)
}

func (s *Server) deleteDatasource(w http.ResponseWriter, r *http.Request) error {
	id, err := pathUUID(r, "id")
	if err != nil {
		return err
	}
	err = s.store.InTenantTx(r.Context(), func(ctx context.Context, tx repo.Tx) error {
		return repo.DeleteDatasource(ctx, tx, id)
	})
	if err != nil {
		return err
	}
	w.WriteHeader(http.StatusNoContent)
	return nil
}

// putCredential 直连凭据设置/轮换；响应永不回显任何凭据字段。
func (s *Server) putCredential(w http.ResponseWriter, r *http.Request) error {
	id, err := pathUUID(r, "id")
	if err != nil {
		return err
	}
	body, err := readBody(r)
	if err != nil {
		return err
	}
	var req credentialReq
	if err := decodeStrict(body, &req); err != nil {
		return err
	}
	if req.Username == "" || req.Password == "" {
		return apierror.New(apierror.CodeValidationFailed).WithDetails(
			apierror.Detail{Field: "credential", Reason: "username 与 password 必填"})
	}

	err = s.store.InTenantTx(r.Context(), func(ctx context.Context, tx repo.Tx) error {
		ds, err := repo.GetDatasource(ctx, tx, id)
		if err != nil {
			return err
		}
		if ds.ConnectMode != "direct" {
			return apierror.New(apierror.CodeDatasourceModeMismatch).WithDetails(
				apierror.Detail{Field: "connect_mode", Reason: "仅直连模式数据源可设置凭据"})
		}
		ciphertext, err := s.sealer.Seal([]byte(req.Password))
		if err != nil {
			return apierror.Wrap(apierror.CodeInternalError, err)
		}
		return repo.RotateDatasourceCredential(ctx, tx, id, req.Username, ciphertext, s.sealer.KeyID())
	})
	if err != nil {
		return err
	}
	w.WriteHeader(http.StatusNoContent)
	return nil
}

// nilIfEmpty 空串 → nil（可选外键入参）。
func nilIfEmpty(s string) *string {
	if s == "" {
		return nil
	}
	return &s
}
