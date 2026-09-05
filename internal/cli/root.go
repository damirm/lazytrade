package cli

import (
	appconfig "github.com/damirm/lazytrade/internal/config"
	"github.com/spf13/cobra"
)

var (
	version = "dev"
	commit  = "unknown"
)

// Execute runs the lazytrade command-line interface.
func Execute() error {
	if err := appconfig.LoadDotEnv(".env"); err != nil {
		return err
	}
	return newRootCommand().Execute()
}

func newRootCommand() *cobra.Command {
	command := &cobra.Command{
		Use:           "lazytrade",
		Short:         "Automated trading, market terminal, and strategy backtesting",
		SilenceErrors: true,
		SilenceUsage:  true,
	}

	command.AddCommand(
		newVersionCommand(),
		newAgentCommand(),
		newTerminalCommand(),
		newBacktestCommand(),
		newDataCommand(),
		newAccountCommand(),
		newConfigCommand(),
		newDBCommand(),
	)
	return command
}
