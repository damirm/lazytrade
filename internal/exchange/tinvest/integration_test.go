//go:build integration

package tinvest

import (
	"context"
	"os"
	"path/filepath"
	"testing"
	"time"
)

func TestSandboxReadOnlyInstruments(t *testing.T) {
	token := os.Getenv("TINVEST_SANDBOX_TOKEN")
	if token == "" {
		t.Skip("TINVEST_SANDBOX_TOKEN is not available")
	}

	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	caCertPath := os.Getenv("TINVEST_CA_CERT_PATH")
	if caCertPath == "" {
		caCertPath = filepath.Join("..", "..", "..", "misc", "certs", "russiantrustedca")
	}
	adapter, err := Open(ctx, Config{
		Name:         "tinvest-integration",
		Token:        token,
		Endpoint:     sandboxEndpoint,
		CACertPath:   caCertPath,
		UnaryTimeout: 20 * time.Second,
	})
	if err != nil {
		t.Fatalf("open sandbox adapter: %v", err)
	}
	defer func() {
		if err := adapter.Close(); err != nil {
			t.Errorf("close sandbox adapter: %v", err)
		}
	}()

	instruments, err := adapter.Instruments(ctx)
	if err != nil {
		t.Fatalf("list sandbox instruments: %v", err)
	}
	if len(instruments) == 0 {
		t.Fatal("sandbox returned no base shares")
	}
}
