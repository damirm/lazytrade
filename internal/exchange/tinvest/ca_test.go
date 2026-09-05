package tinvest

import (
	"crypto/x509"
	"os"
	"path/filepath"
	"testing"
)

func TestAppendCertificatesFromProjectDirectory(t *testing.T) {
	path := filepath.Join("..", "..", "..", "misc", "certs", "russiantrustedca")
	pool := x509.NewCertPool()
	count, err := appendCertificates(pool, path)
	if err != nil {
		t.Fatalf("append certificates: %v", err)
	}
	if count != 4 {
		t.Fatalf("certificate file count = %d, want 4", count)
	}
	if len(pool.Subjects()) == 0 {
		t.Fatal("certificate pool has no subjects")
	}
}

func TestAppendCertificatesRejectsInvalidFile(t *testing.T) {
	path := filepath.Join(t.TempDir(), "invalid.pem")
	if err := os.WriteFile(path, []byte("not a certificate"), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := appendCertificates(x509.NewCertPool(), path); err == nil {
		t.Fatal("invalid certificate was accepted")
	}
}

func TestLoadRootCAsWithoutConfiguredPathUsesPlatformDefault(t *testing.T) {
	pool, err := loadRootCAs("")
	if err != nil {
		t.Fatal(err)
	}
	if pool != nil {
		t.Fatal("empty CA path must leave RootCAs nil")
	}
}
