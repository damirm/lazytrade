package app

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/damirm/lazytrade/internal/storage"
	"github.com/damirm/lazytrade/internal/storage/sqlite"
)

func TestRunBacktestsWritesDeterministicArtifacts(t *testing.T) {
	t.Parallel()
	root := t.TempDir()
	configPath := writeFixture(t, root)
	output := filepath.Join(root, "result")

	first, err := RunBacktests(context.Background(), BacktestOptions{ConfigPath: configPath, Output: output})
	if err != nil {
		t.Fatalf("RunBacktests() error = %v", err)
	}
	if len(first) != 1 || first[0].RunID != "fixture" {
		t.Fatalf("results = %#v", first)
	}
	report1, err := os.ReadFile(filepath.Join(output, "report.json"))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(filepath.Join(output, "trades.csv")); err != nil {
		t.Fatal(err)
	}
	var artifact struct {
		SchemaVersion uint32 `json:"schema_version"`
		ConfigSHA256  string `json:"config_sha256"`
		DatasetSHA256 string `json:"dataset_sha256"`
	}
	if err := json.Unmarshal(report1, &artifact); err != nil {
		t.Fatal(err)
	}
	if artifact.SchemaVersion != 1 || len(artifact.ConfigSHA256) != 64 || len(artifact.DatasetSHA256) != 64 {
		t.Fatalf("artifact metadata = %#v", artifact)
	}
	if _, err := RunBacktests(context.Background(), BacktestOptions{ConfigPath: configPath, Output: output}); err != nil {
		t.Fatal(err)
	}
	report2, err := os.ReadFile(filepath.Join(output, "report.json"))
	if err != nil {
		t.Fatal(err)
	}
	if string(report1) != string(report2) {
		t.Fatal("report is not deterministic")
	}
}

func TestRunBacktestsSupportsPeriodicInvestment(t *testing.T) {
	t.Parallel()
	root := t.TempDir()
	csv := "timestamp,open,high,low,close,volume\n" +
		"2025-01-10T00:00:00Z,10,10,10,10,1\n" +
		"2025-01-10T00:01:00Z,11,11,11,11,1\n" +
		"2025-01-10T00:02:00Z,12,12,12,12,1\n"
	if err := os.WriteFile(filepath.Join(root, "prices.csv"), []byte(csv), 0o600); err != nil {
		t.Fatal(err)
	}
	yaml := `version: 1
exchanges:
  fake:
    type: tinvest
    sandbox: true
agent:
  strategies:
    - id: monthly
      exchange: fake
      instrument: TEST
      strategy:
        type: periodic_investment
        params:
          candle_interval: 1m
          day_of_month: 10
          time: "00:01"
          timezone: UTC
      execution:
        quantity: "2"
        order_type: market
      trading_day:
        timezone: UTC
        reset_at: "00:00"
backtest:
  runs:
    - id: dca
      strategy: monthly
      data:
        type: csv
        path: prices.csv
        interval: 1m
        price_asset: USD
        timezone: UTC
        tick_size: "0.01"
        lot_size: "1"
        gap_policy: fail
      execution:
        initial_cash: {amount: "1000", asset: USD}
        commission: {type: percent, value: "0"}
        slippage: {type: basis_points, value: "0"}
        market_fill: next_open
        limit_fill: touch
      output: {directory: output, json: true, trades_csv: true}
`
	configPath := filepath.Join(root, "config.yaml")
	if err := os.WriteFile(configPath, []byte(yaml), 0o600); err != nil {
		t.Fatal(err)
	}
	results, err := RunBacktests(context.Background(), BacktestOptions{ConfigPath: configPath})
	if err != nil {
		t.Fatal(err)
	}
	if len(results) != 1 || len(results[0].Report.Orders) != 1 || len(results[0].Report.Executions) != 1 {
		t.Fatalf("DCA backtest result = %#v", results)
	}
}

func TestRunBacktestsPersistsCompletedLifecycleAndArtifacts(t *testing.T) {
	t.Parallel()
	root := t.TempDir()
	configPath := writeFixture(t, root)
	store, err := sqlite.Open(context.Background(), filepath.Join(root, "runs.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	if _, err := RunBacktests(context.Background(), BacktestOptions{
		ConfigPath: configPath, Output: filepath.Join(root, "result"), Store: store, Version: "test",
	}); err != nil {
		t.Fatal(err)
	}
	runs, err := store.ListBacktestRuns(context.Background(), 10)
	if err != nil {
		t.Fatal(err)
	}
	if len(runs) != 1 || runs[0].Status != storage.BacktestCompleted || len(runs[0].Artifacts) != 2 {
		t.Fatalf("persisted runs = %#v", runs)
	}
	if len(runs[0].Metrics) == 0 || len(runs[0].Warnings) == 0 {
		t.Fatalf("terminal JSON was not persisted: %#v", runs[0])
	}
}

func TestRunBacktestsPersistsFailure(t *testing.T) {
	t.Parallel()
	root := t.TempDir()
	configPath := writeFixture(t, root)
	if err := os.WriteFile(filepath.Join(root, "prices.csv"), []byte("broken\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	store, err := sqlite.Open(context.Background(), filepath.Join(root, "runs.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	if _, err := RunBacktests(context.Background(), BacktestOptions{ConfigPath: configPath, Store: store}); err == nil {
		t.Fatal("expected dataset failure")
	}
	runs, err := store.ListBacktestRuns(context.Background(), 10)
	if err != nil {
		t.Fatal(err)
	}
	if len(runs) != 1 || runs[0].Status != storage.BacktestFailed || runs[0].ErrorCode == "" {
		t.Fatalf("persisted runs = %#v", runs)
	}
}

func TestRunBacktestsUsesManifestAndVerifiesChecksum(t *testing.T) {
	t.Parallel()
	root := t.TempDir()
	configPath := writeFixture(t, root)
	datasetPath := filepath.Join(root, "prices.csv")
	checksum, err := hashFile(datasetPath)
	if err != nil {
		t.Fatal(err)
	}
	manifest := DatasetManifest{
		SchemaVersion: DatasetManifestSchemaVersion, Source: "tinvest", Exchange: "fake",
		RequestedID: "TEST", InstrumentID: "canonical-test-uid", Symbol: "TEST",
		From: "2025-01-01T00:00:00Z", To: "2025-01-01T00:04:00Z",
		Interval: "1m0s", PriceAsset: "USD", TickSize: "0.01", LotSize: "1",
		Timezone: "UTC", CandleCount: 4, DatasetSHA256: checksum,
		DatasetFile: "prices.csv", OnlyComplete: true,
	}
	payload, err := json.Marshal(manifest)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, "prices.csv.metadata.json"), payload, 0o600); err != nil {
		t.Fatal(err)
	}
	configData, err := os.ReadFile(configPath)
	if err != nil {
		t.Fatal(err)
	}
	oldMetadata := "        interval: 1m\n        price_asset: USD\n        timezone: UTC\n        tick_size: \"0.01\"\n        lot_size: \"1\"\n"
	configData = []byte(strings.Replace(string(configData), oldMetadata, "        metadata_path: prices.csv.metadata.json\n", 1))
	if err := os.WriteFile(configPath, configData, 0o600); err != nil {
		t.Fatal(err)
	}
	results, err := RunBacktests(context.Background(), BacktestOptions{
		ConfigPath: configPath, Output: filepath.Join(root, "result"),
	})
	if err != nil {
		t.Fatalf("RunBacktests() error = %v", err)
	}
	if len(results) != 1 || len(results[0].Report.Orders) == 0 ||
		results[0].Report.Orders[0].InstrumentID != "canonical-test-uid" {
		t.Fatalf("results = %#v", results)
	}

	manifest.DatasetSHA256 = strings.Repeat("0", 64)
	payload, _ = json.Marshal(manifest)
	if err := os.WriteFile(filepath.Join(root, "prices.csv.metadata.json"), payload, 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := RunBacktests(context.Background(), BacktestOptions{
		ConfigPath: configPath, Output: filepath.Join(root, "invalid"),
	}); err == nil || !strings.Contains(err.Error(), "checksum mismatch") {
		t.Fatalf("checksum error = %v", err)
	}
}

func TestRunBacktestsRejectsUnknownRun(t *testing.T) {
	t.Parallel()
	root := t.TempDir()
	_, err := RunBacktests(context.Background(), BacktestOptions{
		ConfigPath: writeFixture(t, root), RunIDs: []string{"missing"},
	})
	if err == nil {
		t.Fatal("expected unknown run error")
	}
}

func TestRunBacktestsHonorsCancellation(t *testing.T) {
	t.Parallel()
	root := t.TempDir()
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	_, err := RunBacktests(ctx, BacktestOptions{ConfigPath: writeFixture(t, root)})
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("error = %v, want context.Canceled", err)
	}
}

func writeFixture(t *testing.T, root string) string {
	t.Helper()
	csv := "timestamp,open,high,low,close,volume\n" +
		"2025-01-01T00:00:00Z,10,11,9,10,1\n" +
		"2025-01-01T00:01:00Z,10,12,9,11,1\n" +
		"2025-01-01T00:02:00Z,11,13,10,12,1\n" +
		"2025-01-01T00:03:00Z,12,13,7,8,1\n"
	if err := os.WriteFile(filepath.Join(root, "prices.csv"), []byte(csv), 0o600); err != nil {
		t.Fatal(err)
	}
	yaml := `version: 1
exchanges:
  fake:
    type: tinvest
    sandbox: true
agent:
  strategies:
    - id: ma
      exchange: fake
      instrument: TEST
      strategy:
        type: moving_average_cross
        params:
          candle_interval: 1m
          fast_period: 1
          slow_period: 2
      execution:
        quantity: "1"
        order_type: market
      trading_day:
        timezone: UTC
        reset_at: "00:00"
backtest:
  runs:
    - id: fixture
      strategy: ma
      data:
        type: csv
        path: prices.csv
        interval: 1m
        price_asset: USD
        timezone: UTC
        tick_size: "0.01"
        lot_size: "1"
        gap_policy: fail
      execution:
        initial_cash:
          amount: "1000"
          asset: USD
        commission:
          type: percent
          value: "0"
        slippage:
          type: basis_points
          value: "0"
        market_fill: next_open
        limit_fill: touch
      output:
        directory: output
        json: true
        trades_csv: true
`
	path := filepath.Join(root, "config.yaml")
	if err := os.WriteFile(path, []byte(yaml), 0o600); err != nil {
		t.Fatal(err)
	}
	return path
}
