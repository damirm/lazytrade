package cli

import (
	appconfig "github.com/damirm/lazytrade/internal/config"
	"github.com/spf13/cobra"
)

func newTerminalCommand() *cobra.Command {
	var configPath string

	command := &cobra.Command{
		Use:   "terminal",
		Short: "Run the read-only market terminal",
		Args:  cobra.NoArgs,
		RunE: func(*cobra.Command, []string) error {
			return commandNotImplemented("terminal", configPath, appconfig.CommandTerminal)
		},
	}
	command.Flags().StringVar(&configPath, "config", "", "path to YAML configuration")
	_ = command.MarkFlagRequired("config")
	return command
}
