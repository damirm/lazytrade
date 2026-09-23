package app

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/damirm/lazytrade/internal/config"
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

func TestRunBacktestsUsesPreparedDatasetAfterSourceMutation(t *testing.T) {
	t.Parallel()
	root := t.TempDir()
	configPath := writeFixture(t, root)
	datasetPath := filepath.Join(root, "prices.csv")
	originalChecksum, err := hashFile(datasetPath)
	if err != nil {
		t.Fatal(err)
	}

	baseline, err := RunBacktests(context.Background(), BacktestOptions{
		ConfigPath: configPath,
		Output:     filepath.Join(root, "baseline"),
	})
	if err != nil {
		t.Fatalf("baseline RunBacktests() error = %v", err)
	}

	replacement := "timestamp,open,high,low,close,volume\n" +
		"2025-01-01T00:00:00Z,50,50,50,50,1\n" +
		"2025-01-01T00:01:00Z,50,50,50,50,1\n" +
		"2025-01-01T00:02:00Z,50,50,50,50,1\n" +
		"2025-01-01T00:03:00Z,50,50,50,50,1\n"
	store := &mutatingBacktestStore{
		start: func(run storage.BacktestRun) error {
			if run.DatasetChecksum != originalChecksum {
				t.Fatalf("persisted start checksum = %q, want %q", run.DatasetChecksum, originalChecksum)
			}
			return os.WriteFile(datasetPath, []byte(replacement), 0o600)
		},
	}
	result, err := RunBacktests(context.Background(), BacktestOptions{
		ConfigPath: configPath,
		Output:     filepath.Join(root, "mutated"),
		Store:      store,
	})
	if err != nil {
		t.Fatalf("RunBacktests() error = %v", err)
	}
	if len(result) != 1 || len(baseline) != 1 {
		t.Fatalf("result counts = %d and %d, want 1 and 1", len(result), len(baseline))
	}
	if !reflect.DeepEqual(result[0].Report.Metrics, baseline[0].Report.Metrics) {
		t.Fatalf("metrics after source mutation = %#v, want %#v", result[0].Report.Metrics, baseline[0].Report.Metrics)
	}
	if !reflect.DeepEqual(result[0].Report.Executions, baseline[0].Report.Executions) {
		t.Fatalf("executions after source mutation = %#v, want %#v", result[0].Report.Executions, baseline[0].Report.Executions)
	}
	if store.finished.Status != storage.BacktestCompleted {
		t.Fatalf("persisted finish status = %q, want %q", store.finished.Status, storage.BacktestCompleted)
	}

	reportData, err := os.ReadFile(result[0].ReportPath)
	if err != nil {
		t.Fatal(err)
	}
	var artifact reportArtifact
	if err := json.Unmarshal(reportData, &artifact); err != nil {
		t.Fatal(err)
	}
	if artifact.DatasetSHA256 != originalChecksum {
		t.Fatalf("report dataset checksum = %q, want %q", artifact.DatasetSHA256, originalChecksum)
	}
	if artifact.DatasetPath != "prices.csv" {
		t.Fatalf("report dataset path = %q, want configured relative path", artifact.DatasetPath)
	}
	replacementChecksum, err := hashFile(datasetPath)
	if err != nil {
		t.Fatal(err)
	}
	if replacementChecksum == originalChecksum {
		t.Fatal("replacement dataset unexpectedly has the original checksum")
	}
}

func TestPrepareBacktestRunResolvesPathsAndCleansSnapshot(t *testing.T) {
	t.Parallel()
	root := t.TempDir()
	configPath := writeFixture(t, root)
	cfg, err := config.LoadFileFor(configPath, config.CommandBacktest)
	if err != nil {
		t.Fatal(err)
	}
	prepared, err := prepareBacktestRun(
		BacktestOptions{ConfigPath: configPath},
		cfg.Backtest.Runs[0],
		cfg.Agent.Strategies[0],
		1,
	)
	if err != nil {
		t.Fatalf("prepareBacktestRun() error = %v", err)
	}
	t.Cleanup(func() { _ = prepared.cleanup() })
	snapshotPath := prepared.snapshotPath
	for name, path := range map[string]string{
		"config":   prepared.configDir,
		"dataset":  prepared.datasetPath,
		"output":   prepared.outputDir,
		"snapshot": snapshotPath,
	} {
		if !filepath.IsAbs(path) {
			t.Fatalf("%s path = %q, want absolute", name, path)
		}
	}
	if prepared.datasetPath != filepath.Join(root, "prices.csv") {
		t.Fatalf("dataset path = %q", prepared.datasetPath)
	}
	if prepared.outputDir != filepath.Join(root, "output") {
		t.Fatalf("output path = %q", prepared.outputDir)
	}
	if _, err := os.Stat(snapshotPath); err != nil {
		t.Fatalf("snapshot before cleanup: %v", err)
	}
	if err := prepared.cleanup(); err != nil {
		t.Fatalf("first cleanup error = %v", err)
	}
	if _, err := os.Stat(snapshotPath); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("snapshot after cleanup error = %v, want not exist", err)
	}
	if err := prepared.cleanup(); err != nil {
		t.Fatalf("second cleanup error = %v", err)
	}
}

func TestRunBacktestsCleansSnapshotWhenStartPersistenceFails(t *testing.T) {
	root := t.TempDir()
	temporaryRoot := filepath.Join(root, "tmp")
	if err := os.Mkdir(temporaryRoot, 0o700); err != nil {
		t.Fatal(err)
	}
	t.Setenv("TMPDIR", temporaryRoot)
	configPath := writeFixture(t, root)
	want := errors.New("start persistence failed")
	store := &mutatingBacktestStore{
		start: func(storage.BacktestRun) error { return want },
	}

	_, err := RunBacktests(context.Background(), BacktestOptions{ConfigPath: configPath, Store: store})
	if !errors.Is(err, want) {
		t.Fatalf("RunBacktests() error = %v, want %v", err, want)
	}
	if store.finished.RunID != "" {
		t.Fatalf("FinishBacktestRun unexpectedly called with %#v", store.finished)
	}
	matches, err := filepath.Glob(filepath.Join(temporaryRoot, "lazytrade-backtest-dataset-*"))
	if err != nil {
		t.Fatal(err)
	}
	if len(matches) != 0 {
		t.Fatalf("temporary snapshots after start persistence failure = %v", matches)
	}
}

func TestPrepareBacktestRunCleansSnapshotAfterManifestFailure(t *testing.T) {
	root := t.TempDir()
	temporaryRoot := filepath.Join(root, "tmp")
	if err := os.Mkdir(temporaryRoot, 0o700); err != nil {
		t.Fatal(err)
	}
	t.Setenv("TMPDIR", temporaryRoot)
	configPath := writeFixture(t, root)
	cfg, err := config.LoadFileFor(configPath, config.CommandBacktest)
	if err != nil {
		t.Fatal(err)
	}
	run := cfg.Backtest.Runs[0]
	run.Data.MetadataPath = "missing.metadata.json"

	_, err = prepareBacktestRun(BacktestOptions{ConfigPath: configPath}, run, cfg.Agent.Strategies[0], 1)
	if err == nil || !strings.Contains(err.Error(), "read dataset manifest") {
		t.Fatalf("prepareBacktestRun() error = %v", err)
	}
	matches, err := filepath.Glob(filepath.Join(temporaryRoot, "lazytrade-backtest-dataset-*"))
	if err != nil {
		t.Fatal(err)
	}
	if len(matches) != 0 {
		t.Fatalf("temporary snapshots after preparation failure = %v", matches)
	}
}

func TestRunBacktestsMissingDatasetReturnsPreparedPathError(t *testing.T) {
	t.Parallel()
	root := t.TempDir()
	configPath := writeFixture(t, root)
	if err := os.Remove(filepath.Join(root, "prices.csv")); err != nil {
		t.Fatal(err)
	}
	store := &mutatingBacktestStore{
		start: func(storage.BacktestRun) error {
			t.Fatal("StartBacktestRun must not be called for a missing source dataset")
			return nil
		},
	}
	_, err := RunBacktests(context.Background(), BacktestOptions{ConfigPath: configPath, Store: store})
	if err == nil || !strings.Contains(err.Error(), `backtest run "fixture": prepare dataset snapshot: open source dataset`) {
		t.Fatalf("RunBacktests() error = %v", err)
	}
}

func TestResolvePreparedBacktestPathsReturnsAbsError(t *testing.T) {
	want := errors.New("absolute path unavailable")
	_, err := resolvePreparedBacktestPathsWithAbs(
		BacktestOptions{ConfigPath: "config.yaml"},
		config.BacktestRun{ID: "fixture"},
		1,
		func(string) (string, error) { return "", want },
	)
	if !errors.Is(err, want) || !strings.Contains(err.Error(), "resolve config directory") {
		t.Fatalf("resolvePreparedBacktestPathsWithAbs() error = %v", err)
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
	store := &mutatingBacktestStore{}
	if _, err := RunBacktests(context.Background(), BacktestOptions{
		ConfigPath: configPath, Output: filepath.Join(root, "invalid"), Store: store,
	}); err == nil || !strings.Contains(err.Error(), "checksum mismatch") {
		t.Fatalf("checksum error = %v", err)
	}
	if store.started.DatasetChecksum != checksum {
		t.Fatalf("persisted start checksum = %q, want actual snapshot checksum %q", store.started.DatasetChecksum, checksum)
	}
	if store.finished.Status != storage.BacktestFailed || store.finished.ErrorCode != "execution_failed" {
		t.Fatalf("persisted checksum mismatch finish = %#v", store.finished)
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

func TestRunBacktestsFinalizesCancelledRunWithBoundedLiveContext(t *testing.T) {
	t.Parallel()
	type contextKey struct{}
	root := t.TempDir()
	ctx, cancel := context.WithCancel(context.WithValue(context.Background(), contextKey{}, "preserved"))
	store := &mutatingBacktestStore{
		start: func(storage.BacktestRun) error {
			cancel()
			return nil
		},
		finish: func(finishCtx context.Context, finish storage.FinishBacktestRun) error {
			if err := finishCtx.Err(); err != nil {
				t.Fatalf("FinishBacktestRun context initially has error %v", err)
			}
			deadline, ok := finishCtx.Deadline()
			if !ok || time.Until(deadline) <= 0 {
				t.Fatalf("FinishBacktestRun deadline = %v, %v; want a live finite deadline", deadline, ok)
			}
			if got := finishCtx.Value(contextKey{}); got != "preserved" {
				t.Fatalf("FinishBacktestRun context value = %v, want preserved", got)
			}
			if finish.Status != storage.BacktestCancelled || finish.ErrorCode != "cancelled" {
				t.Fatalf("FinishBacktestRun terminal state = %#v", finish)
			}
			return nil
		},
	}

	_, err := RunBacktests(ctx, BacktestOptions{ConfigPath: writeFixture(t, root), Store: store})
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("RunBacktests() error = %v, want context.Canceled", err)
	}
	if store.finished.Status != storage.BacktestCancelled || store.finished.ErrorCode != "cancelled" {
		t.Fatalf("persisted finish = %#v", store.finished)
	}
}

func TestFinishBacktestRunWithinHonorsTimeout(t *testing.T) {
	t.Parallel()
	store := &mutatingBacktestStore{
		finish: func(ctx context.Context, _ storage.FinishBacktestRun) error {
			<-ctx.Done()
			return ctx.Err()
		},
	}
	started := time.Now()
	err := finishBacktestRunWithin(
		context.Background(),
		store,
		storage.FinishBacktestRun{RunID: "timeout"},
		10*time.Millisecond,
	)
	if !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("finishBacktestRunWithin() error = %v, want context.DeadlineExceeded", err)
	}
	if elapsed := time.Since(started); elapsed > time.Second {
		t.Fatalf("finishBacktestRunWithin() elapsed = %s, want bounded completion", elapsed)
	}
}

func TestRunBacktestsPreservesExecutionAndFinishErrors(t *testing.T) {
	t.Parallel()
	root := t.TempDir()
	ctx, cancel := context.WithCancel(context.Background())
	store := &mutatingBacktestStore{
		start: func(storage.BacktestRun) error {
			cancel()
			return nil
		},
		finish: func(context.Context, storage.FinishBacktestRun) error {
			return context.DeadlineExceeded
		},
	}

	_, err := RunBacktests(ctx, BacktestOptions{ConfigPath: writeFixture(t, root), Store: store})
	if !errors.Is(err, context.Canceled) || !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("RunBacktests() error = %v, want both context.Canceled and context.DeadlineExceeded", err)
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

type mutatingBacktestStore struct {
	start    func(storage.BacktestRun) error
	finish   func(context.Context, storage.FinishBacktestRun) error
	started  storage.BacktestRun
	finished storage.FinishBacktestRun
}

func (s *mutatingBacktestStore) StartBacktestRun(_ context.Context, run storage.BacktestRun) (storage.BacktestRun, error) {
	s.started = run
	if s.start != nil {
		if err := s.start(run); err != nil {
			return storage.BacktestRun{}, err
		}
	}
	run.Status = storage.BacktestRunning
	run.Revision = 1
	return run, nil
}

func (s *mutatingBacktestStore) FinishBacktestRun(ctx context.Context, finish storage.FinishBacktestRun) (storage.BacktestRun, error) {
	s.finished = finish
	if s.finish != nil {
		if err := s.finish(ctx, finish); err != nil {
			return storage.BacktestRun{}, err
		}
	}
	return storage.BacktestRun{ID: finish.RunID, Status: finish.Status, Revision: finish.ExpectedRevision + 1}, nil
}

func (*mutatingBacktestStore) GetBacktestRun(context.Context, string) (storage.BacktestRun, error) {
	return storage.BacktestRun{}, storage.ErrNotFound
}

func (*mutatingBacktestStore) ListBacktestRuns(context.Context, uint32) ([]storage.BacktestRun, error) {
	return nil, nil
}
