package svcapi

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"regexp"
	"time"

	"github.com/sqlrush/airush/console/internal/enrolltoken"
	"github.com/sqlrush/airush/console/internal/repo"
	"github.com/sqlrush/airush/console/internal/tenancy"
	"github.com/sqlrush/airush/libs/apierror"
)

var uuidRe = regexp.MustCompile(`^[0-9a-fA-F]{8}-[0-9a-fA-F]{4}-[0-9a-fA-F]{4}-[0-9a-fA-F]{4}-[0-9a-fA-F]{12}$`)

type enrollReq struct {
	Token            string `json:"token"`
	CSRPEM           string `json:"csr_pem"`
	ConnectorVersion string `json:"connector_version"`
}

type enrollResp struct {
	ConnectorID    string `json:"connector_id"`
	CertificatePEM string `json:"certificate_pem"`
	CABundlePEM    string `json:"ca_bundle_pem"`
}

// enroll 一次性令牌换证书（spec-1.2 §2.3 第 2 步的控制面侧）。
// 缺失/过期/复用/哈希不符统一 AR_CONNECTOR_ENROLL_TOKEN_INVALID（防枚举）。
func (s *Server) enroll(w http.ResponseWriter, r *http.Request) error {
	var req enrollReq
	if err := decodeJSON(r, &req); err != nil {
		return err
	}
	tok, err := enrolltoken.Parse(req.Token)
	if err != nil || !uuidRe.MatchString(tok.TenantID) || !uuidRe.MatchString(tok.ConnectorID) {
		return apierror.New(apierror.CodeConnectorEnrollTokenInvalid)
	}

	ctx := tenancy.WithTenant(r.Context(), tok.TenantID)
	var resp enrollResp
	err = s.store.InTenantTx(ctx, func(ctx context.Context, tx repo.Tx) error {
		rec, err := repo.GetEnrollmentRecord(ctx, tx, tok.ConnectorID)
		if err != nil {
			// 不存在与令牌无效同响应（防枚举）
			return apierror.New(apierror.CodeConnectorEnrollTokenInvalid)
		}
		switch rec.Status {
		case "revoked":
			return apierror.New(apierror.CodeConnectorRevoked)
		case "pending":
		default:
			return apierror.New(apierror.CodeConnectorAlreadyEnrolled)
		}
		if rec.TokenHash == nil || rec.ExpiresAt == nil ||
			time.Now().After(*rec.ExpiresAt) || !tok.Matches(*rec.TokenHash) {
			return apierror.New(apierror.CodeConnectorEnrollTokenInvalid)
		}

		certPEM, fingerprint, err := s.ca.SignCSR([]byte(req.CSRPEM),
			tok.ConnectorID, tok.TenantID, time.Duration(s.certTTL.connectorCert)*24*time.Hour)
		if err != nil {
			return apierror.Wrap(apierror.CodeValidationFailed, err).WithDetails(
				apierror.Detail{Field: "csr_pem", Reason: "CSR 无效"})
		}
		if err := repo.CompleteEnrollment(ctx, tx, tok.ConnectorID, fingerprint, req.ConnectorVersion); err != nil {
			return err
		}
		resp = enrollResp{
			ConnectorID:    tok.ConnectorID,
			CertificatePEM: string(certPEM),
			CABundlePEM:    string(s.ca.BundlePEM()),
		}
		return nil
	})
	if err != nil {
		return err
	}
	return writeJSON(w, http.StatusOK, resp)
}

type handshakeReq struct {
	TenantID    string `json:"tenant_id"`
	ConnectorID string `json:"connector_id"`
	Fingerprint string `json:"fingerprint"`
}

// handshake 会话建立校验：证书链之外的第二道绑定（指纹 + 状态，spec-1.2 T5）。
func (s *Server) handshake(w http.ResponseWriter, r *http.Request) error {
	var req handshakeReq
	if err := decodeJSON(r, &req); err != nil {
		return err
	}
	if err := validateIDs(req.TenantID, req.ConnectorID); err != nil {
		return err
	}
	ctx := tenancy.WithTenant(r.Context(), req.TenantID)
	err := s.store.InTenantTx(ctx, func(ctx context.Context, tx repo.Tx) error {
		c, err := repo.GetConnector(ctx, tx, req.ConnectorID)
		if err != nil {
			return err
		}
		if c.Status == "revoked" {
			return apierror.New(apierror.CodeConnectorRevoked)
		}
		if c.Status == "pending" {
			return apierror.New(apierror.CodeCommonConflict).WithDetails(
				apierror.Detail{Field: "status", Reason: "接入器尚未注册"})
		}
		if c.CertFingerprint == "" || c.CertFingerprint != req.Fingerprint {
			return apierror.New(apierror.CodeAuthForbidden).WithDetails(
				apierror.Detail{Field: "fingerprint", Reason: "证书指纹与登记不符"})
		}
		return nil
	})
	if err != nil {
		return err
	}
	w.WriteHeader(http.StatusNoContent)
	return nil
}

type statusReq struct {
	TenantID    string     `json:"tenant_id"`
	ConnectorID string     `json:"connector_id"`
	Status      string     `json:"status"`
	HeartbeatAt *time.Time `json:"heartbeat_at"`
}

// status 会话状态迁移记录（gateway 判定、console 落库，spec-1.2 §3）。
func (s *Server) status(w http.ResponseWriter, r *http.Request) error {
	var req statusReq
	if err := decodeJSON(r, &req); err != nil {
		return err
	}
	if err := validateIDs(req.TenantID, req.ConnectorID); err != nil {
		return err
	}
	switch req.Status {
	case "online", "degraded", "offline":
	default:
		return apierror.New(apierror.CodeValidationFailed).WithDetails(
			apierror.Detail{Field: "status", Reason: "必须是 online/degraded/offline"})
	}
	ctx := tenancy.WithTenant(r.Context(), req.TenantID)
	err := s.store.InTenantTx(ctx, func(ctx context.Context, tx repo.Tx) error {
		return repo.UpdateConnectorStatus(ctx, tx, req.ConnectorID, req.Status, req.HeartbeatAt)
	})
	if err != nil {
		return err
	}
	w.WriteHeader(http.StatusNoContent)
	return nil
}

func validateIDs(tenantID, connectorID string) error {
	if !uuidRe.MatchString(tenantID) || !uuidRe.MatchString(connectorID) {
		return apierror.New(apierror.CodeValidationFailed).WithDetails(
			apierror.Detail{Field: "tenant_id/connector_id", Reason: "必须是 UUID"})
	}
	return nil
}

func decodeJSON(r *http.Request, dst any) error {
	dec := json.NewDecoder(http.MaxBytesReader(nil, r.Body, 1<<20))
	dec.DisallowUnknownFields()
	if err := dec.Decode(dst); err != nil {
		return apierror.Wrap(apierror.CodeValidationFailed, err).WithDetails(
			apierror.Detail{Field: "body", Reason: "JSON 解析失败或含未知字段"})
	}
	return nil
}

func writeJSON(w http.ResponseWriter, status int, v any) error {
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.WriteHeader(status)
	if err := json.NewEncoder(w).Encode(v); err != nil {
		return fmt.Errorf("encode response: %w", err)
	}
	return nil
}

// isUUID 复用本包既有的 uuidRe（与 httpapi 同一正则，两包互不引用各自一份）。
func isUUID(s string) bool { return uuidRe.MatchString(s) }
