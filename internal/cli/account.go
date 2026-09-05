package cli

import (
	"errors"
	"fmt"
	"time"

	appconfig "github.com/damirm/lazytrade/internal/config"
	"github.com/damirm/lazytrade/internal/domain"
	"github.com/damirm/lazytrade/internal/exchange/tinvest"
	"github.com/spf13/cobra"
)

func newAccountCommand() *cobra.Command {
	command := &cobra.Command{Use: "account", Short: "Inspect exchange accounts"}
	command.AddCommand(newAccountListCommand(), newAccountCreateCommand(), newAccountPayInCommand(), newAccountSmokeTestCommand())
	return command
}

func newAccountListCommand() *cobra.Command {
	var configPath, exchangeID string
	command := &cobra.Command{
		Use:   "list",
		Short: "List T-Invest sandbox accounts",
		Args:  cobra.NoArgs,
		RunE: func(command *cobra.Command, _ []string) error {
			cfg, err := appconfig.LoadFile(configPath)
			if err != nil {
				return err
			}
			exchangeConfig, err := accountListExchange(cfg, exchangeID)
			if err != nil {
				return err
			}
			token, err := requiredEnvironment(exchangeConfig.TokenEnv)
			if err != nil {
				return err
			}
			adapter, err := tinvest.Open(command.Context(), tinvest.Config{
				Name: exchangeID, Token: token,
				CACertPath: resolveConfigPath(configPath, exchangeConfig.CACertPath),
			})
			if err != nil {
				return err
			}
			defer adapter.Close()
			accounts, err := adapter.SandboxAccounts(command.Context())
			if err != nil {
				return err
			}
			_, _ = fmt.Fprintln(command.OutOrStdout(), "ID\tNAME\tSTATUS\tACCESS\tOPENED_AT")
			for _, account := range accounts {
				opened := "-"
				if account.OpenedAt != nil {
					opened = account.OpenedAt.Format(time.RFC3339)
				}
				_, _ = fmt.Fprintf(command.OutOrStdout(), "%s\t%s\t%s\t%s\t%s\n",
					account.ID, account.Name, account.Status, account.AccessLevel, opened)
			}
			return nil
		},
	}
	command.Flags().StringVar(&configPath, "config", "", "path to YAML configuration")
	command.Flags().StringVar(&exchangeID, "exchange", "", "configured exchange ID")
	_ = command.MarkFlagRequired("config")
	_ = command.MarkFlagRequired("exchange")
	return command
}

func newAccountCreateCommand() *cobra.Command {
	var configPath, exchangeID, name string
	command := &cobra.Command{
		Use:   "create",
		Short: "Create a T-Invest sandbox account",
		Args:  cobra.NoArgs,
		RunE: func(command *cobra.Command, _ []string) error {
			adapter, err := openSandboxAccountAdapter(command, configPath, exchangeID)
			if err != nil {
				return err
			}
			defer adapter.Close()
			accountID, err := adapter.CreateSandboxAccount(command.Context(), name)
			if err != nil {
				return err
			}
			_, _ = fmt.Fprintf(command.OutOrStdout(), "ACCOUNT_ID\t%s\n", accountID)
			return nil
		},
	}
	command.Flags().StringVar(&configPath, "config", "", "path to YAML configuration")
	command.Flags().StringVar(&exchangeID, "exchange", "", "configured exchange ID")
	command.Flags().StringVar(&name, "name", "", "sandbox account name")
	_ = command.MarkFlagRequired("config")
	_ = command.MarkFlagRequired("exchange")
	_ = command.MarkFlagRequired("name")
	return command
}

func newAccountPayInCommand() *cobra.Command {
	var configPath, exchangeID, accountID, amount string
	command := &cobra.Command{
		Use:   "pay-in",
		Short: "Fund a T-Invest sandbox account with virtual RUB",
		Args:  cobra.NoArgs,
		RunE: func(command *cobra.Command, _ []string) error {
			cfg, err := appconfig.LoadFile(configPath)
			if err != nil {
				return err
			}
			exchangeConfig, err := accountListExchange(cfg, exchangeID)
			if err != nil {
				return err
			}
			if accountID == "" {
				if exchangeConfig.AccountIDEnv == "" {
					return errors.New("provide --account-id or configure account_id_env")
				}
				accountID, err = requiredEnvironment(exchangeConfig.AccountIDEnv)
				if err != nil {
					return err
				}
			}
			value, err := domain.NewMoney(amount, "RUB")
			if err != nil || !value.Amount.IsPositive() {
				return errors.New("--amount must be a positive decimal RUB amount")
			}
			adapter, err := openConfiguredSandboxAdapter(command, configPath, exchangeID, exchangeConfig)
			if err != nil {
				return err
			}
			defer adapter.Close()
			balance, err := adapter.PayInSandbox(command.Context(), accountID, value)
			if err != nil {
				return err
			}
			_, _ = fmt.Fprintf(command.OutOrStdout(), "ACCOUNT_ID\t%s\nBALANCE\t%s %s\n",
				accountID, balance.Amount, balance.Asset)
			return nil
		},
	}
	command.Flags().StringVar(&configPath, "config", "", "path to YAML configuration")
	command.Flags().StringVar(&exchangeID, "exchange", "", "configured exchange ID")
	command.Flags().StringVar(&accountID, "account-id", "", "sandbox account ID (defaults to account_id_env)")
	command.Flags().StringVar(&amount, "amount", "", "positive virtual RUB amount")
	_ = command.MarkFlagRequired("config")
	_ = command.MarkFlagRequired("exchange")
	_ = command.MarkFlagRequired("amount")
	return command
}

func openSandboxAccountAdapter(command *cobra.Command, configPath, exchangeID string) (*tinvest.Adapter, error) {
	cfg, err := appconfig.LoadFile(configPath)
	if err != nil {
		return nil, err
	}
	exchangeConfig, err := accountListExchange(cfg, exchangeID)
	if err != nil {
		return nil, err
	}
	return openConfiguredSandboxAdapter(command, configPath, exchangeID, exchangeConfig)
}

func openConfiguredSandboxAdapter(
	command *cobra.Command,
	configPath, exchangeID string,
	exchangeConfig appconfig.ExchangeConfig,
) (*tinvest.Adapter, error) {
	token, err := requiredEnvironment(exchangeConfig.TokenEnv)
	if err != nil {
		return nil, err
	}
	return tinvest.Open(command.Context(), tinvest.Config{
		Name: exchangeID, Token: token,
		CACertPath: resolveConfigPath(configPath, exchangeConfig.CACertPath),
	})
}

func accountListExchange(cfg appconfig.Config, exchangeID string) (appconfig.ExchangeConfig, error) {
	exchangeConfig, exists := cfg.Exchanges[exchangeID]
	if !exists {
		return appconfig.ExchangeConfig{}, fmt.Errorf("exchange %q is not configured", exchangeID)
	}
	if exchangeConfig.Type != "tinvest" {
		return appconfig.ExchangeConfig{}, fmt.Errorf("exchange %q is not T-Invest", exchangeID)
	}
	if !exchangeConfig.Sandbox || exchangeConfig.AllowLiveTrading {
		return appconfig.ExchangeConfig{}, errors.New("account list currently supports sandbox mode only")
	}
	if exchangeConfig.TokenEnv == "" {
		return appconfig.ExchangeConfig{}, errors.New("T-Invest token_env is required")
	}
	return exchangeConfig, nil
}
