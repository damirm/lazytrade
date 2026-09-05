package app

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"time"

	"github.com/damirm/lazytrade/internal/backtest"
	"github.com/damirm/lazytrade/internal/config"
	"github.com/damirm/lazytrade/internal/domain"
)

type ValidateDataOptions struct {
	Input        string
	MetadataPath string
	GapPolicy    backtest.GapPolicy
}

type ValidateDataResult struct {
	DatasetPath  string
	ManifestPath string
	Rows         uint64
	Gaps         []backtest.Gap
	SHA256       string
	Manifest     DatasetManifest
}

// ValidateCandleData performs the same structural and integrity checks used by
// backtest without requiring exchange credentials or a trading configuration.
func ValidateCandleData(ctx context.Context, options ValidateDataOptions) (ValidateDataResult, error) {
	if err := ctx.Err(); err != nil {
		return ValidateDataResult{}, err
	}
	if options.Input == "" {
		return ValidateDataResult{}, errors.New("dataset input path is required")
	}
	inputPath, err := filepath.Abs(options.Input)
	if err != nil {
		return ValidateDataResult{}, fmt.Errorf("resolve dataset input: %w", err)
	}
	manifestPath := options.MetadataPath
	if manifestPath == "" {
		manifestPath = inputPath + ".metadata.json"
	} else if !filepath.IsAbs(manifestPath) {
		manifestPath, err = filepath.Abs(manifestPath)
		if err != nil {
			return ValidateDataResult{}, fmt.Errorf("resolve metadata path: %w", err)
		}
	}
	manifest, err := loadDatasetManifest(manifestPath)
	if err != nil {
		return ValidateDataResult{}, err
	}
	requestedID := manifest.RequestedID
	if requestedID == "" {
		requestedID = manifest.InstrumentID
	}
	run := config.BacktestRun{
		Data: config.BacktestData{
			Path: inputPath, MetadataPath: manifestPath,
			GapPolicy: string(options.GapPolicy),
		},
		Execution: config.BacktestExecution{
			InitialCash: config.MoneyConfig{Amount: "1", Asset: manifest.PriceAsset},
		},
	}
	strategy := config.StrategyConfig{
		Exchange: manifest.Exchange, Instrument: requestedID,
		Strategy: config.StrategyDefinition{Params: config.MovingAverageCrossParams{
			CandleInterval: manifest.Interval,
		}},
	}
	metadata, err := resolveDatasetMetadata("", inputPath, run, strategy)
	if err != nil {
		return ValidateDataResult{}, err
	}
	policy := options.GapPolicy
	if policy == "" {
		policy = backtest.GapMark
	}
	switch policy {
	case backtest.GapFail, backtest.GapAllow, backtest.GapMark:
	default:
		return ValidateDataResult{}, fmt.Errorf("unsupported gap policy %q", policy)
	}
	input, err := os.Open(inputPath)
	if err != nil {
		return ValidateDataResult{}, fmt.Errorf("open dataset: %w", err)
	}
	defer input.Close()
	iterator, err := backtest.NewCSVIterator(input, backtest.DatasetMetadata{
		Version: 1, ExchangeAccountID: domain.ExchangeAccountID(manifest.Exchange),
		InstrumentID: metadata.InstrumentID, Interval: metadata.Interval,
		PriceAsset: metadata.PriceAsset, Timezone: metadata.Timezone,
		TimestampLayout: time.RFC3339,
		TickSize:        metadata.TickSize, LotSize: metadata.LotSize,
		GapPolicy: policy, ExpectedSHA256: metadata.ExpectedSHA,
	})
	if err != nil {
		return ValidateDataResult{}, err
	}
	for {
		_, nextErr := iterator.Next(ctx)
		if errors.Is(nextErr, io.EOF) {
			break
		}
		if nextErr != nil {
			return ValidateDataResult{}, nextErr
		}
	}
	if iterator.Rows() != metadata.ExpectedRows {
		return ValidateDataResult{}, fmt.Errorf("dataset manifest: candle_count is %d, CSV contains %d rows", metadata.ExpectedRows, iterator.Rows())
	}
	if iterator.First().Before(metadata.RangeFrom) || !iterator.Last().Before(metadata.RangeTo) {
		return ValidateDataResult{}, errors.New("dataset manifest: CSV timestamps are outside the declared range")
	}
	checksum, err := iterator.Checksum()
	if err != nil {
		return ValidateDataResult{}, err
	}
	return ValidateDataResult{
		DatasetPath: inputPath, ManifestPath: metadata.ManifestPath,
		Rows: iterator.Rows(), Gaps: iterator.Gaps(), SHA256: checksum, Manifest: manifest,
	}, nil
}
