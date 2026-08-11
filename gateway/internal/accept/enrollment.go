package accept

import (
	"context"
	"errors"
	"log/slog"

	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"

	"github.com/sqlrush/airush/gateway/internal/consoleclient"
	connectorv1 "github.com/sqlrush/airush/proto/gen/go/connector/v1"
)

// EnrollmentServer 实现 server-TLS 的注册 RPC；纯转发到 console 内部 API。
type EnrollmentServer struct {
	connectorv1.UnimplementedEnrollmentServiceServer
	console *consoleclient.Client
	logger  *slog.Logger
}

// NewEnrollmentServer 构造。
func NewEnrollmentServer(console *consoleclient.Client, logger *slog.Logger) *EnrollmentServer {
	return &EnrollmentServer{console: console, logger: logger}
}

// Enroll 转发到 console；错误码映射为 gRPC status（details 保留 AR_ 码）。
func (s *EnrollmentServer) Enroll(ctx context.Context, req *connectorv1.EnrollRequest) (*connectorv1.EnrollResponse, error) {
	if req.GetEnrollmentToken() == "" || len(req.GetCsrPem()) == 0 {
		return nil, status.Error(codes.InvalidArgument, "AR_VALIDATION_FAILED")
	}
	res, err := s.console.Enroll(ctx, req.GetEnrollmentToken(), string(req.GetCsrPem()), req.GetConnectorVersion())
	if err != nil {
		return nil, enrollGRPCError(err)
	}
	return &connectorv1.EnrollResponse{
		ConnectorId:    res.ConnectorID,
		CertificatePem: []byte(res.CertificatePEM),
		CaBundlePem:    []byte(res.CABundlePEM),
	}, nil
}

// enrollGRPCError 把 console APIError 转 gRPC status，码字符串进 message 供客户端识别。
func enrollGRPCError(err error) error {
	var apiErr *consoleclient.APIError
	if errors.As(err, &apiErr) {
		code := codes.Internal
		switch apiErr.Status {
		case 400:
			code = codes.InvalidArgument
		case 401, 403:
			code = codes.PermissionDenied
		case 409:
			code = codes.FailedPrecondition
		}
		return status.Error(code, apiErr.Code)
	}
	return status.Error(codes.Unavailable, "AR_INTERNAL_ERROR")
}
