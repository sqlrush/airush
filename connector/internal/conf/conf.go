// Package conf 是 connector 的本地配置与凭据存储（spec-1.2 D5）。
// 私钥/证书仅存客户侧（0600 权限）；配置目录默认 ~/.airush-connector。
package conf

import (
	"fmt"
	"os"
	"path/filepath"
)

const (
	dirPerm  = 0o700
	filePerm = 0o600

	keyFile  = "connector.key"
	certFile = "connector.crt"
	caFile   = "ca-bundle.crt"
	metaFile = "connector.id"
)

// Store 是客户侧凭据目录。
type Store struct{ dir string }

// NewStore 解析配置目录（override 为空时用默认）。
func NewStore(override string) (*Store, error) {
	dir := override
	if dir == "" {
		home, err := os.UserHomeDir()
		if err != nil {
			return nil, fmt.Errorf("conf: resolve home: %w", err)
		}
		dir = filepath.Join(home, ".airush-connector")
	}
	if err := os.MkdirAll(dir, dirPerm); err != nil {
		return nil, fmt.Errorf("conf: mkdir %s: %w", dir, err)
	}
	return &Store{dir: dir}, nil
}

// WriteKey 落私钥（0600）。
func (s *Store) WriteKey(pem []byte) error { return s.write(keyFile, pem) }

// ReadKey 读私钥。
func (s *Store) ReadKey() ([]byte, error) { return s.read(keyFile) }

// WriteCert 落客户端证书。
func (s *Store) WriteCert(pem []byte) error { return s.write(certFile, pem) }

// ReadCert 读客户端证书。
func (s *Store) ReadCert() ([]byte, error) { return s.read(certFile) }

// WriteCABundle 落会话端信任链。
func (s *Store) WriteCABundle(pem []byte) error { return s.write(caFile, pem) }

// ReadCABundle 读信任链。
func (s *Store) ReadCABundle() ([]byte, error) { return s.read(caFile) }

// WriteConnectorID 落 connector id。
func (s *Store) WriteConnectorID(id string) error { return s.write(metaFile, []byte(id)) }

// ReadConnectorID 读 connector id。
func (s *Store) ReadConnectorID() (string, error) {
	b, err := s.read(metaFile)
	return string(b), err
}

// Enrolled 判断是否已注册（证书 + 私钥 + id 齐备）。
func (s *Store) Enrolled() bool {
	for _, f := range []string{keyFile, certFile, metaFile} {
		if _, err := os.Stat(filepath.Join(s.dir, f)); err != nil {
			return false
		}
	}
	return true
}

func (s *Store) write(name string, data []byte) error {
	path := filepath.Join(s.dir, name)
	if err := os.WriteFile(path, data, filePerm); err != nil {
		return fmt.Errorf("conf: write %s: %w", name, err)
	}
	return nil
}

func (s *Store) read(name string) ([]byte, error) {
	// name 是包内固定常量集（非外部输入），路径限定在 store 目录内。
	data, err := os.ReadFile(filepath.Join(s.dir, name)) //nolint:gosec // 固定文件名，非用户输入
	if err != nil {
		return nil, fmt.Errorf("conf: read %s: %w", name, err)
	}
	return data, nil
}
