package cli

import (
	"fmt"

	appconfig "github.com/damirm/lazytrade/internal/config"
	"github.com/spf13/cobra"
)

func newConfigCommand() *cobra.Command {
	command := &cobra.Command{
		Use:   "config",
		Short: "Inspect and validate configuration",
	}
	command.AddCommand(newConfigValidateCommand())
	return command
}

func newConfigValidateCommand() *cobra.Command {
	var configPath string
	var target string

	command := &cobra.Command{
		Use:   "validate",
		Short: "Strictly validate a YAML configuration",
		Args:  cobra.NoArgs,
		RunE: func(command *cobra.Command, _ []string) error {
			if target == "" {
				if _, err := appconfig.LoadFile(configPath); err != nil {
					return err
				}
			} else {
				validationCommand, err := configCommand(target)
				if err != nil {
					return err
				}
				if _, err := appconfig.LoadFileFor(configPath, validationCommand); err != nil {
					return err
				}
			}
			fmt.Fprintln(command.OutOrStdout(), "configuration is valid")
			return nil
		},
	}
	command.Flags().StringVar(&configPath, "config", "", "path to YAML configuration")
	command.Flags().StringVar(&target, "for", "", "validate runtime requirements for agent, terminal, or backtest")
	_ = command.MarkFlagRequired("config")
	return command
}

func configCommand(value string) (appconfig.Command, error) {
	switch value {
	case string(appconfig.CommandAgent):
		return appconfig.CommandAgent, nil
	case string(appconfig.CommandTerminal):
		return appconfig.CommandTerminal, nil
	case string(appconfig.CommandBacktest):
		return appconfig.CommandBacktest, nil
	default:
		return "", fmt.Errorf("config validate --for: unsupported command %q", value)
	}
}
