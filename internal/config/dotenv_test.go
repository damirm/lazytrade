package config

import (
	"os"
	"path/filepath"
	"testing"
)

func TestLoadDotEnvLoadsValuesAndPreservesEnvironment(t *testing.T) {
	path := filepath.Join(t.TempDir(), ".env")
	if err := os.WriteFile(path, []byte(`
# local settings
LAZYTRADE_DOTENV_PLAIN=value
export LAZYTRADE_DOTENV_QUOTED="hello\nworld"
LAZYTRADE_DOTENV_EXISTING=from-file
`), 0o600); err != nil {
		t.Fatal(err)
	}
	for _, name := range []string{"LAZYTRADE_DOTENV_PLAIN", "LAZYTRADE_DOTENV_QUOTED"} {
		_ = os.Unsetenv(name)
		t.Cleanup(func() { _ = os.Unsetenv(name) })
	}
	t.Setenv("LAZYTRADE_DOTENV_EXISTING", "from-environment")
	if err := LoadDotEnv(path); err != nil {
		t.Fatal(err)
	}
	if value := os.Getenv("LAZYTRADE_DOTENV_PLAIN"); value != "value" {
		t.Fatalf("plain value = %q", value)
	}
	if value := os.Getenv("LAZYTRADE_DOTENV_QUOTED"); value != "hello\nworld" {
		t.Fatalf("quoted value = %q", value)
	}
	if value := os.Getenv("LAZYTRADE_DOTENV_EXISTING"); value != "from-environment" {
		t.Fatalf("existing value = %q", value)
	}
}

func TestLoadDotEnvIgnoresMissingFile(t *testing.T) {
	if err := LoadDotEnv(filepath.Join(t.TempDir(), "missing.env")); err != nil {
		t.Fatal(err)
	}
}

func TestLoadDotEnvRejectsMalformedEntry(t *testing.T) {
	path := filepath.Join(t.TempDir(), ".env")
	if err := os.WriteFile(path, []byte("NOT VALID\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := LoadDotEnv(path); err == nil {
		t.Fatal("malformed dotenv entry was accepted")
	}
}
