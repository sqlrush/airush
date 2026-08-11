// Package enroll 是 connector 注册流程（spec-1.2 §2.3 客户侧）：本地生成密钥对，
// 仅上送 CSR，换取 mTLS 客户端证书。私钥永不离开本机。
package enroll

import (
	"context"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/pem"
	"fmt"

	"google.golang.org/grpc"
	"google.golang.org/grpc/credentials"

	"github.com/sqlrush/airush/connector/internal/conf"
	connectorv1 "github.com/sqlrush/airush/proto/gen/go/connector/v1"
)

// Run 执行注册：生成密钥→CSR→Enroll RPC→落证书/私钥/CA 到本地 store。
func Run(ctx context.Context, store *conf.Store, gatewayAddr, token, version string, tlsCfg credentials.TransportCredentials) error {
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		return fmt.Errorf("enroll: generate key: %w", err)
	}
	csrDER, err := x509.CreateCertificateRequest(rand.Reader,
		&x509.CertificateRequest{Subject: pkix.Name{CommonName: "pending"}}, key)
	if err != nil {
		return fmt.Errorf("enroll: create CSR: %w", err)
	}
	csrPEM := pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE REQUEST", Bytes: csrDER})

	conn, err := grpc.NewClient(gatewayAddr, grpc.WithTransportCredentials(tlsCfg))
	if err != nil {
		return fmt.Errorf("enroll: dial gateway: %w", err)
	}
	defer func() { _ = conn.Close() }()

	resp, err := connectorv1.NewEnrollmentServiceClient(conn).Enroll(ctx, &connectorv1.EnrollRequest{
		EnrollmentToken:  token,
		CsrPem:           csrPEM,
		ConnectorVersion: version,
	})
	if err != nil {
		return fmt.Errorf("enroll: RPC: %w", err)
	}

	keyDER, err := x509.MarshalECPrivateKey(key)
	if err != nil {
		return fmt.Errorf("enroll: marshal key: %w", err)
	}
	keyPEM := pem.EncodeToMemory(&pem.Block{Type: "EC PRIVATE KEY", Bytes: keyDER})

	if err := store.WriteKey(keyPEM); err != nil {
		return err
	}
	if err := store.WriteCert(resp.GetCertificatePem()); err != nil {
		return err
	}
	if err := store.WriteCABundle(resp.GetCaBundlePem()); err != nil {
		return err
	}
	return store.WriteConnectorID(resp.GetConnectorId())
}
