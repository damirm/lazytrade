package cli

import (
	"testing"

	appconfig "github.com/damirm/lazytrade/internal/config"
)

func TestAccountCommandExposesExplicitSandboxOperations(t *testing.T) {
	command := newAccountCommand()
	for _, name := range []string{"list", "create", "pay-in", "smoke-test"} {
		found := false
		for _, child := range command.Commands() {
			if child.Name() == name {
				found = true
				break
			}
		}
		if !found {
			t.Fatalf("account subcommand %q is missing", name)
		}
	}
}

func TestAccountListExchangeDoesNotRequireAccountID(t *testing.T) {
	cfg := appconfig.Config{Exchanges: map[string]appconfig.ExchangeConfig{
		"sandbox": {Type: "tinvest", Sandbox: true, TokenEnv: "TOKEN"},
	}}
	exchangeConfig, err := accountListExchange(cfg, "sandbox")
	if err != nil {
		t.Fatal(err)
	}
	if exchangeConfig.AccountIDEnv != "" {
		t.Fatalf("account ID env = %q", exchangeConfig.AccountIDEnv)
	}
}

func TestAccountListExchangeRejectsNonSandbox(t *testing.T) {
	cfg := appconfig.Config{Exchanges: map[string]appconfig.ExchangeConfig{
		"live": {Type: "tinvest", TokenEnv: "TOKEN", AllowLiveTrading: true},
	}}
	if _, err := accountListExchange(cfg, "live"); err == nil {
		t.Fatal("production exchange was accepted")
	}
}
