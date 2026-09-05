package cli

import (
	"fmt"

	"github.com/spf13/cobra"
)

func newVersionCommand() *cobra.Command {
	return &cobra.Command{
		Use:   "version",
		Short: "Print application version",
		Args:  cobra.NoArgs,
		Run: func(command *cobra.Command, _ []string) {
			fmt.Fprintf(command.OutOrStdout(), "lazytrade %s (%s)\n", version, commit)
		},
	}
}
