package app

import (
	"context"
	"crypto/sha256"
	"encoding/csv"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"github.com/damirm/lazytrade/internal/backtest"
	"github.com/damirm/lazytrade/internal/clock"
	"github.com/damirm/lazytrade/internal/config"
	"github.com/damirm/lazytrade/internal/domain"
	"github.com/damirm/lazytrade/internal/risk"
	"github.com/damirm/lazytrade/internal/storage"
	"github.com/damirm/lazytrade/internal/strategy"
	"github.com/damirm/lazytrade/internal/strategy/builtin"
	"github.com/shopspring/decimal"
)

const ArtifactSchemaVersion = 1

type BacktestOptions struct {
	ConfigPath string
	Output     string
	RunIDs     []string
	Risk       RiskBuilder
	Store      storage.BacktestStore
	Version    string
}

// RiskBuilder is the integration seam for the shared risk manager. It is
// deliberately expressed in backtest contracts so this package never
// reimplements risk rules.
type RiskBuilder func(config.StrategyConfig, config.BacktestRun, clock.Clock) (backtest.RiskEvaluator, error)

type BacktestResult struct {
	RunID         string
	OutputDir     string
	ReportPath    string
	TradesCSVPath string
	Report        backtest.Report
}

type reportArtifact struct {
	SchemaVersion  uint32          `json:"schema_version"`
	RunID          string          `json:"run_id"`
	StrategyID     string          `json:"strategy_id"`
	ConfigSHA256   string          `json:"config_sha256"`
	DatasetSHA256  string          `json:"dataset_sha256"`
	DatasetPath    string          `json:"dataset_path"`
	DatasetGaps    []backtest.Gap  `json:"dataset_gaps"`
	BacktestReport backtest.Report `json:"report"`
}

func RunBacktests(ctx context.Context, options BacktestOptions) ([]BacktestResult, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	if strings.TrimSpace(options.ConfigPath) == "" {
		return nil, errors.New("config path is required")
	}
	cfg, err := config.LoadFileFor(options.ConfigPath, config.CommandBacktest)
	if err != nil {
		return nil, err
	}
	configHash, err := canonicalConfigHash(cfg)
	if err != nil {
		return nil, fmt.Errorf("hash config: %w", err)
	}
	runs, err := selectRuns(cfg.Backtest.Runs, options.RunIDs)
	if err != nil {
		return nil, err
	}
	strategies := make(map[string]config.StrategyConfig, len(cfg.Agent.Strategies))
	for _, item := range cfg.Agent.Strategies {
		strategies[item.ID] = item
	}
	results := make([]BacktestResult, 0, len(runs))
	for _, run := range runs {
		if err := ctx.Err(); err != nil {
			return results, err
		}
		startedAt := time.Now().UTC()
		var stored storage.BacktestRun
		if options.Store != nil {
			datasetPath := resolvePath(mustAbsoluteDir(options.ConfigPath), run.Data.Path)
			datasetHash, hashErr := hashFile(datasetPath)
			if hashErr != nil {
				return results, fmt.Errorf("backtest run %q dataset checksum: %w", run.ID, hashErr)
			}
			version := options.Version
			if version == "" {
				version = "dev"
			}
			stored, err = options.Store.StartBacktestRun(ctx, storage.BacktestRun{
				ID: executionID(run.ID, configHash, startedAt), ConfiguredRunID: run.ID,
				StrategyID: run.Strategy, ApplicationVersion: version,
				ConfigHash: configHash, DatasetChecksum: datasetHash, StartedAt: startedAt,
			})
			if err != nil {
				return results, fmt.Errorf("start persisted backtest run %q: %w", run.ID, err)
			}
		}
		result, err := runBacktest(ctx, options, run, strategies[run.Strategy], configHash, len(runs))
		if options.Store != nil {
			status, code := storage.BacktestCompleted, ""
			if err != nil {
				status, code = storage.BacktestFailed, "execution_failed"
				if errors.Is(err, context.Canceled) {
					status, code = storage.BacktestCancelled, "cancelled"
				}
			}
			finish := storage.FinishBacktestRun{
				RunID: stored.ID, ExpectedRevision: stored.Revision, Status: status,
				FinishedAt: time.Now().UTC(), ErrorCode: code,
			}
			if err != nil {
				finish.ErrorMessage = err.Error()
			} else {
				finish.Metrics, _ = json.Marshal(result.Report.Metrics)
				finish.Warnings, _ = json.Marshal(result.Report.Warnings)
				finish.Artifacts, err = artifactManifests(result, finish.FinishedAt)
				if err != nil {
					finish.Status = storage.BacktestFailed
					finish.ErrorCode = "artifact_manifest_failed"
					finish.ErrorMessage = err.Error()
					finish.Metrics, finish.Warnings, finish.Artifacts = nil, nil, nil
				}
			}
			_, finishErr := options.Store.FinishBacktestRun(context.WithoutCancel(ctx), finish)
			if finishErr != nil {
				return results, errors.Join(err, fmt.Errorf("finish persisted backtest run %q: %w", run.ID, finishErr))
			}
		}
		if err != nil {
			return results, fmt.Errorf("backtest run %q: %w", run.ID, err)
		}
		results = append(results, result)
	}
	return results, nil
}

func runBacktest(ctx context.Context, options BacktestOptions, run config.BacktestRun, strategyConfig config.StrategyConfig, configHash string, runCount int) (BacktestResult, error) {
	configDir, err := filepath.Abs(filepath.Dir(options.ConfigPath))
	if err != nil {
		return BacktestResult{}, err
	}
	dataPath := resolvePath(configDir, run.Data.Path)
	input, err := os.Open(dataPath)
	if err != nil {
		return BacktestResult{}, fmt.Errorf("open dataset: %w", err)
	}
	defer input.Close()

	metadata, err := resolveDatasetMetadata(configDir, dataPath, run, strategyConfig)
	if err != nil {
		return BacktestResult{}, err
	}
	asset, err := domain.NormalizeAsset(run.Execution.InitialCash.Asset)
	if err != nil {
		return BacktestResult{}, err
	}
	initialCash, err := domain.NewMoney(run.Execution.InitialCash.Amount, asset)
	if err != nil {
		return BacktestResult{}, err
	}
	commission, err := decimal.NewFromString(run.Execution.Commission.Value)
	if err != nil {
		return BacktestResult{}, err
	}
	slippage, err := decimal.NewFromString(run.Execution.Slippage.Value)
	if err != nil {
		return BacktestResult{}, err
	}
	instrumentID := metadata.InstrumentID
	accountID := domain.ExchangeAccountID(strategyConfig.Exchange)
	iterator, err := backtest.NewCSVIterator(input, backtest.DatasetMetadata{
		Version: 1, ExchangeAccountID: accountID, InstrumentID: instrumentID,
		Interval: metadata.Interval, PriceAsset: metadata.PriceAsset, Timezone: metadata.Timezone,
		TimestampLayout: time.RFC3339, TickSize: metadata.TickSize,
		LotSize: metadata.LotSize, GapPolicy: backtest.GapPolicy(run.Data.GapPolicy),
		ExpectedSHA256: metadata.ExpectedSHA,
	})
	if err != nil {
		return BacktestResult{}, err
	}
	strategyConfig.Strategy.Params.CandleInterval = metadata.Interval.String()
	implementation, err := builtin.Build(strategyConfig)
	if err != nil {
		return BacktestResult{}, err
	}
	worker, err := strategy.NewWorker(domain.StrategyID(strategyConfig.ID), accountID, instrumentID, implementation, strategy.NewMemoryStatePort())
	if err != nil {
		return BacktestResult{}, err
	}
	brokerConfig := backtest.BrokerConfig{
		InitialCash: initialCash, InstrumentID: instrumentID,
		TickSize: metadata.TickSize, LotSize: metadata.LotSize,
		CommissionPercent: commission, SlippageBPS: slippage,
	}
	broker, err := backtest.NewSimulatedBroker(brokerConfig)
	if err != nil {
		return BacktestResult{}, err
	}
	virtualClock := clock.NewVirtual(time.Time{})
	var evaluator backtest.RiskEvaluator
	if options.Risk != nil {
		evaluator, err = options.Risk(strategyConfig, run, virtualClock)
		if err != nil {
			return BacktestResult{}, fmt.Errorf("build risk evaluator: %w", err)
		}
		if evaluator == nil {
			return BacktestResult{}, errors.New("risk builder returned nil evaluator")
		}
	} else {
		evaluator, err = buildManagedRisk(strategyConfig, run, virtualClock)
		if err != nil {
			return BacktestResult{}, fmt.Errorf("build shared risk evaluator: %w", err)
		}
	}
	report, err := (backtest.Runner{
		Iterator: iterator, Clock: virtualClock,
		Strategy: worker, Risk: evaluator, Broker: broker,
	}).Run(ctx)
	if err != nil {
		return BacktestResult{}, err
	}
	if metadata.ManifestPath != "" {
		if iterator.Rows() != metadata.ExpectedRows {
			return BacktestResult{}, fmt.Errorf("dataset manifest: candle_count is %d, CSV contains %d rows", metadata.ExpectedRows, iterator.Rows())
		}
		if iterator.First().Before(metadata.RangeFrom) || !iterator.Last().Before(metadata.RangeTo) {
			return BacktestResult{}, errors.New("dataset manifest: CSV timestamps are outside the declared range")
		}
	}
	datasetHash, err := iterator.Checksum()
	if err != nil {
		return BacktestResult{}, err
	}
	outputDir, err := outputDirectory(configDir, options.Output, run, runCount)
	if err != nil {
		return BacktestResult{}, err
	}
	if err := os.MkdirAll(outputDir, 0o750); err != nil {
		return BacktestResult{}, fmt.Errorf("create output directory: %w", err)
	}
	result := BacktestResult{RunID: run.ID, OutputDir: outputDir}
	result.Report = report
	if run.Output.JSON {
		payload, err := json.MarshalIndent(reportArtifact{
			SchemaVersion: ArtifactSchemaVersion, RunID: run.ID, StrategyID: run.Strategy,
			ConfigSHA256: configHash, DatasetSHA256: datasetHash,
			DatasetPath: filepath.Clean(run.Data.Path), DatasetGaps: iterator.Gaps(),
			BacktestReport: report,
		}, "", "  ")
		if err != nil {
			return BacktestResult{}, err
		}
		payload = append(payload, '\n')
		result.ReportPath = filepath.Join(outputDir, "report.json")
		if err := writeAtomic(result.ReportPath, func(writer io.Writer) error {
			_, err := writer.Write(payload)
			return err
		}); err != nil {
			return BacktestResult{}, err
		}
	}
	if run.Output.TradesCSV {
		result.TradesCSVPath = filepath.Join(outputDir, "trades.csv")
		if err := writeAtomic(result.TradesCSVPath, func(writer io.Writer) error {
			return writeTradesCSV(writer, report.Executions)
		}); err != nil {
			return BacktestResult{}, err
		}
	}
	return result, nil
}

func mustAbsoluteDir(configPath string) string {
	absolute, err := filepath.Abs(filepath.Dir(configPath))
	if err != nil {
		return filepath.Dir(configPath)
	}
	return absolute
}

func executionID(runID, configHash string, startedAt time.Time) string {
	sum := sha256.Sum256([]byte(runID + "\x00" + configHash + "\x00" + startedAt.Format(time.RFC3339Nano)))
	return hex.EncodeToString(sum[:])
}

func hashFile(path string) (string, error) {
	input, err := os.Open(path)
	if err != nil {
		return "", err
	}
	defer input.Close()
	digest := sha256.New()
	if _, err := io.Copy(digest, input); err != nil {
		return "", err
	}
	return hex.EncodeToString(digest.Sum(nil)), nil
}

func artifactManifests(result BacktestResult, createdAt time.Time) ([]storage.BacktestArtifact, error) {
	type artifactInput struct {
		kind, path, media string
	}
	inputs := []artifactInput{
		{kind: "report", path: result.ReportPath, media: "application/json"},
		{kind: "trades", path: result.TradesCSVPath, media: "text/csv"},
	}
	artifacts := make([]storage.BacktestArtifact, 0, len(inputs))
	for _, input := range inputs {
		if input.path == "" {
			continue
		}
		checksum, err := hashFile(input.path)
		if err != nil {
			return nil, err
		}
		info, err := os.Stat(input.path)
		if err != nil {
			return nil, err
		}
		idSum := sha256.Sum256([]byte(result.RunID + "\x00" + input.kind + "\x00" + checksum))
		artifacts = append(artifacts, storage.BacktestArtifact{
			ID: hex.EncodeToString(idSum[:]), ArtifactType: input.kind, Path: input.path,
			Checksum: checksum, SizeBytes: info.Size(), SchemaVersion: ArtifactSchemaVersion,
			MediaType: input.media, CreatedAt: createdAt,
		})
	}
	return artifacts, nil
}

func buildManagedRisk(strategyConfig config.StrategyConfig, run config.BacktestRun, runtimeClock clock.Clock) (backtest.RiskEvaluator, error) {
	policy, err := risk.NewTradingDayPolicy(strategyConfig.TradingDay.Timezone, strategyConfig.TradingDay.ResetAt)
	if err != nil {
		return nil, err
	}
	asset, err := domain.NormalizeAsset(run.Execution.InitialCash.Asset)
	if err != nil {
		return nil, err
	}
	riskConfig := risk.Config{
		StrategyID: domain.StrategyID(strategyConfig.ID), SettlementAsset: asset,
		TradingDay: policy,
	}
	if configured := strategyConfig.Risk.MaxPositionValue; configured != nil {
		value, err := domain.NewMoney(configured.Amount, configured.Asset)
		if err != nil {
			return nil, err
		}
		riskConfig.MaxPositionValue = &value
	}
	if configured := strategyConfig.Risk.MaxDailyLoss; configured != nil {
		value, err := domain.NewMoney(configured.Amount, configured.Asset)
		if err != nil {
			return nil, err
		}
		riskConfig.MaxDailyLoss = &risk.DailyLossLimit{
			Limit: value, Mode: risk.PnLMode(configured.PnL),
		}
	}
	return backtest.NewManagedRiskEvaluator(riskConfig, runtimeClock)
}

func selectRuns(all []config.BacktestRun, requested []string) ([]config.BacktestRun, error) {
	byID := make(map[string]config.BacktestRun, len(all))
	for _, run := range all {
		byID[run.ID] = run
	}
	if len(requested) == 0 {
		result := append([]config.BacktestRun(nil), all...)
		sort.Slice(result, func(i, j int) bool { return result[i].ID < result[j].ID })
		return result, nil
	}
	seen := make(map[string]struct{}, len(requested))
	result := make([]config.BacktestRun, 0, len(requested))
	for _, id := range requested {
		if _, duplicate := seen[id]; duplicate {
			continue
		}
		run, ok := byID[id]
		if !ok {
			return nil, fmt.Errorf("backtest run %q is not defined", id)
		}
		seen[id] = struct{}{}
		result = append(result, run)
	}
	return result, nil
}

func outputDirectory(configDir, override string, run config.BacktestRun, runCount int) (string, error) {
	if run.ID == "." || run.ID == ".." || filepath.Base(run.ID) != run.ID || strings.ContainsAny(run.ID, `/\`) {
		return "", fmt.Errorf("run ID %q is unsafe for an output path", run.ID)
	}
	if override == "" {
		return resolvePath(configDir, run.Output.Directory), nil
	}
	root, err := filepath.Abs(override)
	if err != nil {
		return "", err
	}
	if runCount == 1 {
		return root, nil
	}
	return filepath.Join(root, run.ID), nil
}

func resolvePath(base, path string) string {
	if filepath.IsAbs(path) {
		return filepath.Clean(path)
	}
	return filepath.Join(base, filepath.Clean(path))
}

func canonicalConfigHash(cfg config.Config) (string, error) {
	payload, err := json.Marshal(cfg)
	if err != nil {
		return "", err
	}
	sum := sha256.Sum256(payload)
	return hex.EncodeToString(sum[:]), nil
}

func writeAtomic(path string, write func(io.Writer) error) (returnErr error) {
	temp, err := os.CreateTemp(filepath.Dir(path), "."+filepath.Base(path)+".tmp-*")
	if err != nil {
		return fmt.Errorf("create temporary artifact: %w", err)
	}
	tempPath := temp.Name()
	defer func() {
		_ = temp.Close()
		if returnErr != nil {
			_ = os.Remove(tempPath)
		}
	}()
	if err := write(temp); err != nil {
		return err
	}
	if err := temp.Sync(); err != nil {
		return err
	}
	if err := temp.Close(); err != nil {
		return err
	}
	if err := os.Chmod(tempPath, 0o640); err != nil {
		return err
	}
	if err := os.Rename(tempPath, path); err != nil {
		return fmt.Errorf("publish artifact: %w", err)
	}
	return nil
}

func writeTradesCSV(output io.Writer, executions []domain.Execution) error {
	writer := csv.NewWriter(output)
	if err := writer.Write([]string{"execution_id", "order_id", "strategy_id", "instrument_id", "side", "quantity", "price", "asset", "commission", "executed_at"}); err != nil {
		return err
	}
	for _, execution := range executions {
		if err := writer.Write([]string{
			string(execution.ID), string(execution.OrderID), string(execution.StrategyID),
			string(execution.InstrumentID), orderSide(execution.Side), execution.Quantity.Value.String(),
			execution.Price.Value.String(), execution.Price.Asset, execution.Commission.Amount.String(),
			execution.ExecutedAt.UTC().Format(time.RFC3339Nano),
		}); err != nil {
			return err
		}
	}
	writer.Flush()
	return writer.Error()
}

func orderSide(side domain.OrderSide) string {
	if side == domain.OrderSideBuy {
		return "buy"
	}
	return "sell"
}
