package cli

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/damirm/lazytrade/internal/app"
	"github.com/damirm/lazytrade/internal/backtest"
	appconfig "github.com/damirm/lazytrade/internal/config"
	"github.com/damirm/lazytrade/internal/domain"
	"github.com/damirm/lazytrade/internal/exchange/tinvest"
	"github.com/spf13/cobra"
)

func newDataCommand() *cobra.Command {
	command := &cobra.Command{Use: "data", Short: "Import and validate historical market data"}
	command.AddCommand(newDataDownloadCommand(), newDataValidateCommand())
	return command
}

func newDataValidateCommand() *cobra.Command {
	var input, metadata, gapPolicy string
	command := &cobra.Command{
		Use:   "validate",
		Short: "Validate an OHLCV dataset and its integrity manifest",
		Args:  cobra.NoArgs,
		RunE: func(command *cobra.Command, _ []string) error {
			result, err := app.ValidateCandleData(command.Context(), app.ValidateDataOptions{
				Input: input, MetadataPath: metadata,
				GapPolicy: backtest.GapPolicy(gapPolicy),
			})
			if err != nil {
				return err
			}
			_, _ = fmt.Fprintf(command.OutOrStdout(),
				"dataset\t%s\nmetadata\t%s\ncandles\t%d\ngaps\t%d\nsha256\t%s\n",
				result.DatasetPath, result.ManifestPath, result.Rows, len(result.Gaps), result.SHA256)
			return nil
		},
	}
	command.Flags().StringVar(&input, "input", "", "path to the OHLCV CSV dataset")
	command.Flags().StringVar(&metadata, "metadata", "", "manifest path (default: <input>.metadata.json)")
	command.Flags().StringVar(&gapPolicy, "gap-policy", "mark", "gap handling: fail, allow, or mark")
	_ = command.MarkFlagRequired("input")
	return command
}

func newDataDownloadCommand() *cobra.Command {
	var configPath, exchangeID, instrumentID, intervalText, fromText, toText, output string
	command := &cobra.Command{
		Use:   "download",
		Short: "Download an immutable OHLCV dataset from T-Invest",
		Args:  cobra.NoArgs,
		RunE: func(command *cobra.Command, _ []string) error {
			cfg, err := appconfig.LoadFile(configPath)
			if err != nil {
				return err
			}
			exchangeConfig, ok := cfg.Exchanges[exchangeID]
			if !ok {
				return fmt.Errorf("exchange %q is not defined", exchangeID)
			}
			if exchangeConfig.Type != "tinvest" {
				return fmt.Errorf("exchange %q has unsupported type %q", exchangeID, exchangeConfig.Type)
			}
			if !exchangeConfig.Sandbox {
				return fmt.Errorf("exchange %q is not configured for sandbox", exchangeID)
			}
			if strings.TrimSpace(exchangeConfig.TokenEnv) == "" {
				return fmt.Errorf("exchange %q token_env is required", exchangeID)
			}
			token := os.Getenv(exchangeConfig.TokenEnv)
			if token == "" {
				return fmt.Errorf("required environment variable %q is not set", exchangeConfig.TokenEnv)
			}
			interval, err := parseDownloadInterval(intervalText)
			if err != nil {
				return err
			}
			from, err := parseBoundary(fromText)
			if err != nil {
				return fmt.Errorf("parse --from: %w", err)
			}
			to, err := parseBoundary(toText)
			if err != nil {
				return fmt.Errorf("parse --to: %w", err)
			}
			caPath := resolveConfigPath(configPath, exchangeConfig.CACertPath)
			adapter, err := tinvest.Open(command.Context(), tinvest.Config{
				Name: exchangeID, Token: token, CACertPath: caPath,
			})
			if err != nil {
				return err
			}
			defer adapter.Close()
			result, err := app.DownloadCandleData(command.Context(), app.DownloadDataOptions{
				Source: adapter, ExchangeID: exchangeID, InstrumentID: domain.InstrumentID(instrumentID),
				Interval: interval, From: from, To: to, Output: output,
			})
			if err != nil {
				return err
			}
			_, _ = fmt.Fprintf(command.OutOrStdout(),
				"dataset\t%s\nmetadata\t%s\ncandles\t%d\nsha256\t%s\n",
				result.DatasetPath, result.ManifestPath, result.Manifest.CandleCount, result.Manifest.DatasetSHA256)
			return nil
		},
	}
	flags := command.Flags()
	flags.StringVar(&configPath, "config", "", "path to YAML configuration")
	flags.StringVar(&exchangeID, "exchange", "", "configured exchange ID")
	flags.StringVar(&instrumentID, "instrument", "", "T-Invest UID, FIGI, or ticker_class-code")
	flags.StringVar(&intervalText, "interval", "", "candle interval: 1m, 5m, 15m, 1h, or 24h")
	flags.StringVar(&fromText, "from", "", "inclusive UTC date or RFC3339 timestamp")
	flags.StringVar(&toText, "to", "", "exclusive UTC date or RFC3339 timestamp")
	flags.StringVar(&output, "output", "", "destination CSV path")
	for _, name := range []string{"config", "exchange", "instrument", "interval", "from", "to", "output"} {
		_ = command.MarkFlagRequired(name)
	}
	return command
}

func parseBoundary(value string) (time.Time, error) {
	for _, layout := range []string{time.RFC3339Nano, "2006-01-02"} {
		if parsed, err := time.Parse(layout, value); err == nil {
			return parsed.UTC(), nil
		}
	}
	return time.Time{}, errors.New("expected YYYY-MM-DD or RFC3339")
}

func parseDownloadInterval(value string) (time.Duration, error) {
	interval, err := time.ParseDuration(value)
	if err != nil {
		return 0, fmt.Errorf("parse --interval: %w", err)
	}
	switch interval {
	case time.Minute, 5 * time.Minute, 15 * time.Minute, time.Hour, 24 * time.Hour:
		return interval, nil
	default:
		return 0, fmt.Errorf("unsupported candle interval %q", value)
	}
}

func resolveConfigPath(configPath, value string) string {
	if value == "" || filepath.IsAbs(value) {
		return value
	}
	return filepath.Join(filepath.Dir(configPath), value)
}
