package cli

import (
	"bytes"
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/damirm/lazytrade/internal/storage"
	"github.com/damirm/lazytrade/internal/storage/sqlite"
)

func TestBacktestCommandRunsSelectedFixture(t *testing.T) {
	t.Parallel()
	rootDir := t.TempDir()
	csvPath := filepath.Join(rootDir, "prices.csv")
	if err := os.WriteFile(csvPath, []byte("timestamp,open,high,low,close,volume\n2025-01-01T00:00:00Z,10,10,10,10,1\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	configPath := filepath.Join(rootDir, "config.yaml")
	configText := strings.ReplaceAll(`version: 1
database: {driver: sqlite, dsn: runs.db}
exchanges: {fake: {type: tinvest, sandbox: true}}
agent:
  strategies:
    - id: ma
      exchange: fake
      instrument: TEST
      strategy: {type: moving_average_cross, params: {candle_interval: 1m, fast_period: 1, slow_period: 2}}
      execution: {quantity: "1", order_type: market}
      trading_day: {timezone: UTC, reset_at: "00:00"}
backtest:
  runs:
    - id: fixture
      strategy: ma
      data: {type: csv, path: DATA_PATH, interval: 1m, price_asset: USD, timezone: UTC, tick_size: "0.01", lot_size: "1", gap_policy: fail}
      execution:
        initial_cash: {amount: "100", asset: USD}
        commission: {type: percent, value: "0"}
        slippage: {type: basis_points, value: "0"}
        market_fill: next_open
        limit_fill: touch
      output: {directory: ignored, json: true, trades_csv: true}
`, "DATA_PATH", csvPath)
	if err := os.WriteFile(configPath, []byte(configText), 0o600); err != nil {
		t.Fatal(err)
	}
	outputDir := filepath.Join(rootDir, "artifacts")
	command := newRootCommand()
	var stdout bytes.Buffer
	command.SetOut(&stdout)
	command.SetArgs([]string{"backtest", "--config", configPath, "--run", "fixture", "--output", outputDir})
	if err := command.Execute(); err != nil {
		t.Fatalf("Execute() error = %v", err)
	}
	if !strings.Contains(stdout.String(), "fixture") {
		t.Fatalf("stdout = %q", stdout.String())
	}
	for _, name := range []string{"report.json", "trades.csv"} {
		if _, err := os.Stat(filepath.Join(outputDir, name)); err != nil {
			t.Fatal(err)
		}
	}
	store, err := sqlite.Open(context.Background(), filepath.Join(rootDir, "runs.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	runs, err := store.ListBacktestRuns(context.Background(), 10)
	if err != nil {
		t.Fatal(err)
	}
	if len(runs) != 1 || runs[0].Status != storage.BacktestCompleted || len(runs[0].Artifacts) != 2 {
		t.Fatalf("persisted runs = %#v", runs)
	}
}
