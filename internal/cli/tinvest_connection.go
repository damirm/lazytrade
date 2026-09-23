package cli

import (
	"context"
	"errors"

	appconfig "github.com/damirm/lazytrade/internal/config"
	"github.com/damirm/lazytrade/internal/exchange/tinvest"
)

func configuredSandboxTInvestConfig(
	configPath, exchangeID string,
	exchangeConfig appconfig.ExchangeConfig,
	accountID string,
) (tinvest.Config, error) {
	if !sandboxTInvest(exchangeConfig) {
		return tinvest.Config{}, errors.New("T-Invest sandbox configuration is required")
	}
	token, err := requiredEnvironment(exchangeConfig.TokenEnv)
	if err != nil {
		return tinvest.Config{}, err
	}
	return tinvest.Config{
		Name:       exchangeID,
		Token:      token,
		AccountID:  accountID,
		CACertPath: resolveConfigPath(configPath, exchangeConfig.CACertPath),
	}, nil
}

func openConfiguredSandboxTInvest(
	ctx context.Context,
	configPath, exchangeID string,
	exchangeConfig appconfig.ExchangeConfig,
	accountID string,
) (*tinvest.Adapter, error) {
	config, err := configuredSandboxTInvestConfig(configPath, exchangeID, exchangeConfig, accountID)
	if err != nil {
		return nil, err
	}
	return tinvest.Open(ctx, config)
}

func sandboxTInvest(exchangeConfig appconfig.ExchangeConfig) bool {
	return exchangeConfig.Type == "tinvest" && exchangeConfig.Sandbox && !exchangeConfig.AllowLiveTrading
}
