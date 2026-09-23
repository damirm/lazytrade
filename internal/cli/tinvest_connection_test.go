package cli

import (
	"path/filepath"
	"strings"
	"testing"

	appconfig "github.com/damirm/lazytrade/internal/config"
)

func TestConfiguredSandboxTInvestConfig(t *testing.T) {
	const tokenEnv = "R10_TEST_TOKEN"
	t.Setenv(tokenEnv, "synthetic-token")

	configPath := filepath.Join("configs", "agent.yaml")
	valid := appconfig.ExchangeConfig{
		Type: "tinvest", TokenEnv: tokenEnv, CACertPath: "certs/ca.pem", Sandbox: true,
	}

	tests := []struct {
		name      string
		config    appconfig.ExchangeConfig
		accountID string
		wantErr   string
	}{
		{name: "configured account", config: valid, accountID: "account-1"},
		{name: "accountless", config: valid},
		{name: "non T-Invest", config: appconfig.ExchangeConfig{Type: "fake", TokenEnv: "R10_UNSET_TOKEN", Sandbox: true}, wantErr: "T-Invest sandbox"},
		{name: "non sandbox", config: appconfig.ExchangeConfig{Type: "tinvest", TokenEnv: "R10_UNSET_TOKEN"}, wantErr: "T-Invest sandbox"},
		{name: "allow live", config: appconfig.ExchangeConfig{Type: "tinvest", TokenEnv: "R10_UNSET_TOKEN", Sandbox: true, AllowLiveTrading: true}, wantErr: "T-Invest sandbox"},
		{name: "missing token", config: appconfig.ExchangeConfig{Type: "tinvest", TokenEnv: "R10_UNSET_TOKEN", Sandbox: true}, wantErr: "required environment variable"},
		{name: "blank token", config: appconfig.ExchangeConfig{Type: "tinvest", TokenEnv: "R10_BLANK_TOKEN", Sandbox: true}, wantErr: "required environment variable"},
	}
	t.Setenv("R10_BLANK_TOKEN", " \t")

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			got, err := configuredSandboxTInvestConfig(configPath, "sandbox-main", test.config, test.accountID)
			if test.wantErr != "" {
				if err == nil || !strings.Contains(err.Error(), test.wantErr) {
					t.Fatalf("error = %v, want substring %q", err, test.wantErr)
				}
				if strings.Contains(err.Error(), "synthetic-token") {
					t.Fatalf("error exposes token: %v", err)
				}
				return
			}
			if err != nil {
				t.Fatal(err)
			}
			if got.Name != "sandbox-main" || got.Token != "synthetic-token" || got.AccountID != test.accountID {
				t.Fatalf("config identity name=%q account_id=%q", got.Name, got.AccountID)
			}
			wantCA := filepath.Join("configs", "certs/ca.pem")
			if got.CACertPath != wantCA {
				t.Fatalf("CA cert path = %q, want %q", got.CACertPath, wantCA)
			}
		})
	}
}
