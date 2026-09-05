package cli

import "github.com/spf13/cobra"

func newDBCommand() *cobra.Command {
	command := &cobra.Command{
		Use:   "db",
		Short: "Manage the application database",
	}
	command.AddCommand(newDBMigrateCommand())
	return command
}

func newDBMigrateCommand() *cobra.Command {
	var configPath string

	command := &cobra.Command{
		Use:   "migrate",
		Short: "Apply database migrations",
		Args:  cobra.NoArgs,
		RunE: func(*cobra.Command, []string) error {
			return staticCommandNotImplemented("db migrate", configPath)
		},
	}
	command.Flags().StringVar(&configPath, "config", "", "path to YAML configuration")
	_ = command.MarkFlagRequired("config")
	return command
}
