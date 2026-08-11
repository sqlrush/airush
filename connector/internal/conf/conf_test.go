package conf

import (
	"os"
	"path/filepath"
	"testing"
)

func TestStoreRoundTripAndPerms(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	s, err := NewStore(dir)
	if err != nil {
		t.Fatalf("new store: %v", err)
	}
	if s.Enrolled() {
		t.Fatal("empty store reports enrolled")
	}

	if err := s.WriteKey([]byte("KEY")); err != nil {
		t.Fatalf("write key: %v", err)
	}
	if err := s.WriteCert([]byte("CERT")); err != nil {
		t.Fatalf("write cert: %v", err)
	}
	if err := s.WriteCABundle([]byte("CA")); err != nil {
		t.Fatalf("write ca: %v", err)
	}
	if err := s.WriteConnectorID("conn-1"); err != nil {
		t.Fatalf("write id: %v", err)
	}
	if !s.Enrolled() {
		t.Fatal("store with all files not enrolled")
	}

	// 私钥文件 0600
	info, err := os.Stat(filepath.Join(dir, keyFile))
	if err != nil {
		t.Fatalf("stat key: %v", err)
	}
	if info.Mode().Perm() != filePerm {
		t.Fatalf("key perm = %o, want %o", info.Mode().Perm(), filePerm)
	}

	if id, _ := s.ReadConnectorID(); id != "conn-1" {
		t.Fatalf("read id = %q", id)
	}
	if key, _ := s.ReadKey(); string(key) != "KEY" {
		t.Fatal("key roundtrip")
	}
}

func TestReadMissing(t *testing.T) {
	t.Parallel()
	s, _ := NewStore(t.TempDir())
	if _, err := s.ReadCert(); err == nil {
		t.Fatal("read missing cert accepted")
	}
	if _, err := s.ReadConnectorID(); err == nil {
		t.Fatal("read missing id accepted")
	}
}

func TestNewStoreMkdirError(t *testing.T) {
	t.Parallel()
	// override 指向一个已存在的普通文件 → MkdirAll 失败
	f := filepath.Join(t.TempDir(), "afile")
	if err := os.WriteFile(f, []byte("x"), 0o600); err != nil {
		t.Fatalf("write file: %v", err)
	}
	if _, err := NewStore(f); err == nil {
		t.Fatal("NewStore over a file accepted")
	}
}

func TestWriteErrorOnReadOnlyDir(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	s, err := NewStore(dir)
	if err != nil {
		t.Fatalf("new store: %v", err)
	}
	if err := os.Chmod(dir, 0o500); err != nil { //nolint:gosec // 测试：制造只读目录触发写失败
		t.Fatalf("chmod: %v", err)
	}
	t.Cleanup(func() { _ = os.Chmod(dir, 0o700) }) //nolint:gosec // 测试清理恢复权限
	if err := s.WriteKey([]byte("K")); err == nil {
		t.Fatal("write to read-only dir accepted")
	}
}
