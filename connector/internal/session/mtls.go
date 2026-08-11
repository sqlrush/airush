package session

import (
	"crypto/tls"
	"crypto/x509"
	"errors"
	"fmt"

	"google.golang.org/grpc/credentials"
)

// MTLSCreds 组装客户端 mTLS 凭据：客户端证书 + 会话端信任链。
func MTLSCreds(certPEM, keyPEM, caBundlePEM []byte) (credentials.TransportCredentials, error) {
	clientCert, err := tls.X509KeyPair(certPEM, keyPEM)
	if err != nil {
		return nil, fmt.Errorf("session: client keypair: %w", err)
	}
	roots := x509.NewCertPool()
	if !roots.AppendCertsFromPEM(caBundlePEM) {
		return nil, errors.New("session: append CA bundle failed")
	}
	return credentials.NewTLS(&tls.Config{
		Certificates: []tls.Certificate{clientCert},
		RootCAs:      roots,
		MinVersion:   tls.VersionTLS13,
	}), nil
}
