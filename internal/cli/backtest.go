package cli

import (
	"fmt"
	"path/filepath"

	"github.com/damirm/lazytrade/internal/app"
	appconfig "github.com/damirm/lazytrade/internal/config"
	"github.com/damirm/lazytrade/internal/storage"
	"github.com/damirm/lazytrade/internal/storage/sqlite"
	"github.com/spf13/cobra"
)

func newBacktestCommand() *cobra.Command {
	var configPath string
	var outputPath string
	var runIDs []string

	command := &cobra.Command{
		Use:   "backtest",
		Short: "Run strategies against historical data",
		Args:  cobra.NoArgs,
		RunE: func(command *cobra.Command, _ []string) error {
			cfg, err := appconfig.LoadFileFor(configPath, appconfig.CommandBacktest)
			if err != nil {
				return err
			}
			var store storage.BacktestStore
			if cfg.Database.Driver != "" {
				if cfg.Database.Driver != "sqlite" {
					return fmt.Errorf("backtest persistence: unsupported database driver %q", cfg.Database.Driver)
				}
				dsn := cfg.Database.DSN
				if dsn != ":memory:" && !filepath.IsAbs(dsn) {
					dsn = filepath.Join(filepath.Dir(configPath), dsn)
				}
				sqliteStore, openErr := sqlite.Open(command.Context(), dsn)
				err = openErr
				if err != nil {
					return fmt.Errorf("open backtest storage: %w", err)
				}
				defer sqliteStore.Close()
				store = sqliteStore
			}
			results, err := app.RunBacktests(command.Context(), app.BacktestOptions{
				ConfigPath: configPath, Output: outputPath, RunIDs: runIDs,
				Store: store, Version: version,
			})
			if err != nil {
				return err
			}
			for _, result := range results {
				_, _ = fmt.Fprintf(command.OutOrStdout(), "%s\t%s\n", result.RunID, result.OutputDir)
			}
			return nil
		},
	}
	command.Flags().StringVar(&configPath, "config", "", "path to YAML configuration")
	command.Flags().StringVar(&outputPath, "output", "", "output directory for reports")
	command.Flags().StringSliceVar(&runIDs, "run", nil, "backtest run ID (repeatable or comma-separated)")
	_ = command.MarkFlagRequired("config")
	return command
}
