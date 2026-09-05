package app

import (
	"bytes"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"time"

	"github.com/damirm/lazytrade/internal/config"
	"github.com/damirm/lazytrade/internal/domain"
)

type resolvedDatasetMetadata struct {
	InstrumentID domain.InstrumentID
	Interval     time.Duration
	PriceAsset   string
	Timezone     *time.Location
	TickSize     domain.Price
	LotSize      domain.Quantity
	ExpectedSHA  string
	ManifestPath string
	ExpectedRows uint64
	RangeFrom    time.Time
	RangeTo      time.Time
}

func resolveDatasetMetadata(configDir, dataPath string, run config.BacktestRun, strategy config.StrategyConfig) (resolvedDatasetMetadata, error) {
	if run.Data.MetadataPath == "" {
		return metadataFromConfig(run, strategy)
	}
	manifestPath := resolvePath(configDir, run.Data.MetadataPath)
	manifest, err := loadDatasetManifest(manifestPath)
	if err != nil {
		return resolvedDatasetMetadata{}, err
	}
	if manifest.SchemaVersion != DatasetManifestSchemaVersion {
		return resolvedDatasetMetadata{}, fmt.Errorf("dataset manifest: unsupported schema_version %d", manifest.SchemaVersion)
	}
	if manifest.Source == "" {
		return resolvedDatasetMetadata{}, errors.New("dataset manifest: source must not be empty")
	}
	if !manifest.OnlyComplete {
		return resolvedDatasetMetadata{}, errors.New("dataset manifest: incomplete candles are not supported")
	}
	if manifest.CandleCount <= 0 {
		return resolvedDatasetMetadata{}, errors.New("dataset manifest: candle_count must be positive")
	}
	rangeFrom, err := time.Parse(time.RFC3339Nano, manifest.From)
	if err != nil {
		return resolvedDatasetMetadata{}, fmt.Errorf("dataset manifest: invalid from: %w", err)
	}
	rangeTo, err := time.Parse(time.RFC3339Nano, manifest.To)
	if err != nil || !rangeFrom.Before(rangeTo) {
		return resolvedDatasetMetadata{}, errors.New("dataset manifest: to must be after from")
	}
	if manifest.Exchange != strategy.Exchange {
		return resolvedDatasetMetadata{}, fmt.Errorf("dataset manifest: exchange %q does not match strategy exchange %q", manifest.Exchange, strategy.Exchange)
	}
	if manifest.RequestedID != "" && manifest.RequestedID != strategy.Instrument {
		return resolvedDatasetMetadata{}, fmt.Errorf("dataset manifest: requested instrument %q does not match strategy instrument %q", manifest.RequestedID, strategy.Instrument)
	}
	if manifest.RequestedID == "" && manifest.InstrumentID != strategy.Instrument {
		return resolvedDatasetMetadata{}, fmt.Errorf("dataset manifest: instrument %q does not match strategy instrument %q", manifest.InstrumentID, strategy.Instrument)
	}
	if manifest.DatasetFile == "" || filepath.Base(manifest.DatasetFile) != manifest.DatasetFile {
		return resolvedDatasetMetadata{}, fmt.Errorf("dataset manifest: unsafe dataset_file %q", manifest.DatasetFile)
	}
	if filepath.Base(dataPath) != manifest.DatasetFile {
		return resolvedDatasetMetadata{}, fmt.Errorf("dataset manifest: dataset_file %q does not match %q", manifest.DatasetFile, filepath.Base(dataPath))
	}
	if len(manifest.DatasetSHA256) != 64 {
		return resolvedDatasetMetadata{}, errors.New("dataset manifest: dataset_sha256 must contain 64 hexadecimal characters")
	}
	if _, err := hex.DecodeString(manifest.DatasetSHA256); err != nil {
		return resolvedDatasetMetadata{}, fmt.Errorf("dataset manifest: invalid dataset_sha256: %w", err)
	}
	interval, err := time.ParseDuration(manifest.Interval)
	if err != nil || interval <= 0 {
		return resolvedDatasetMetadata{}, fmt.Errorf("dataset manifest: invalid interval %q", manifest.Interval)
	}
	if run.Data.Interval != "" {
		configured, _ := time.ParseDuration(run.Data.Interval)
		if configured != interval {
			return resolvedDatasetMetadata{}, fmt.Errorf("dataset manifest: interval %s does not match configured interval %s", interval, configured)
		}
	}
	strategyInterval, _ := time.ParseDuration(strategy.Strategy.Params.CandleInterval)
	if strategyInterval != interval {
		return resolvedDatasetMetadata{}, fmt.Errorf("dataset manifest: interval %s does not match strategy interval %s", interval, strategyInterval)
	}
	location, err := time.LoadLocation(manifest.Timezone)
	if err != nil {
		return resolvedDatasetMetadata{}, fmt.Errorf("dataset manifest: invalid timezone %q", manifest.Timezone)
	}
	if run.Data.Timezone != "" && run.Data.Timezone != manifest.Timezone {
		return resolvedDatasetMetadata{}, fmt.Errorf("dataset manifest: timezone %q does not match configured timezone %q", manifest.Timezone, run.Data.Timezone)
	}
	asset, err := domain.NormalizeAsset(manifest.PriceAsset)
	if err != nil || asset != manifest.PriceAsset {
		return resolvedDatasetMetadata{}, fmt.Errorf("dataset manifest: invalid price_asset %q", manifest.PriceAsset)
	}
	if asset != run.Execution.InitialCash.Asset {
		return resolvedDatasetMetadata{}, fmt.Errorf("dataset manifest: price_asset %q does not match initial cash asset %q", asset, run.Execution.InitialCash.Asset)
	}
	if run.Data.PriceAsset != "" && run.Data.PriceAsset != asset {
		return resolvedDatasetMetadata{}, fmt.Errorf("dataset manifest: price_asset %q does not match configured value %q", asset, run.Data.PriceAsset)
	}
	tick, err := domain.NewPrice(manifest.TickSize, asset)
	if err != nil {
		return resolvedDatasetMetadata{}, fmt.Errorf("dataset manifest: tick_size: %w", err)
	}
	lot, err := domain.NewQuantity(manifest.LotSize)
	if err != nil || !lot.Value.IsPositive() {
		return resolvedDatasetMetadata{}, fmt.Errorf("dataset manifest: invalid lot_size %q", manifest.LotSize)
	}
	if run.Data.TickSize != "" && run.Data.TickSize != tick.Value.String() {
		configured, _ := domain.NewPrice(run.Data.TickSize, asset)
		if !configured.Value.Equal(tick.Value) {
			return resolvedDatasetMetadata{}, fmt.Errorf("dataset manifest: tick_size %s does not match configured value %s", tick.Value, run.Data.TickSize)
		}
	}
	if run.Data.LotSize != "" {
		configured, _ := domain.NewQuantity(run.Data.LotSize)
		if !configured.Value.Equal(lot.Value) {
			return resolvedDatasetMetadata{}, fmt.Errorf("dataset manifest: lot_size %s does not match configured value %s", lot.Value, run.Data.LotSize)
		}
	}
	return resolvedDatasetMetadata{
		InstrumentID: domain.InstrumentID(manifest.InstrumentID), Interval: interval,
		PriceAsset: asset, Timezone: location, TickSize: tick, LotSize: lot,
		ExpectedSHA: manifest.DatasetSHA256, ManifestPath: manifestPath,
		ExpectedRows: uint64(manifest.CandleCount), RangeFrom: rangeFrom.UTC(), RangeTo: rangeTo.UTC(),
	}, nil
}

func metadataFromConfig(run config.BacktestRun, strategy config.StrategyConfig) (resolvedDatasetMetadata, error) {
	interval, _ := time.ParseDuration(run.Data.Interval)
	location, _ := time.LoadLocation(run.Data.Timezone)
	asset, err := domain.NormalizeAsset(run.Data.PriceAsset)
	if err != nil {
		return resolvedDatasetMetadata{}, err
	}
	tick, err := domain.NewPrice(run.Data.TickSize, asset)
	if err != nil {
		return resolvedDatasetMetadata{}, err
	}
	lot, err := domain.NewQuantity(run.Data.LotSize)
	if err != nil {
		return resolvedDatasetMetadata{}, err
	}
	return resolvedDatasetMetadata{
		InstrumentID: domain.InstrumentID(strategy.Instrument), Interval: interval,
		PriceAsset: asset, Timezone: location, TickSize: tick, LotSize: lot,
	}, nil
}

func loadDatasetManifest(path string) (DatasetManifest, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return DatasetManifest{}, fmt.Errorf("read dataset manifest %q: %w", path, err)
	}
	decoder := json.NewDecoder(bytes.NewReader(data))
	decoder.DisallowUnknownFields()
	var manifest DatasetManifest
	if err := decoder.Decode(&manifest); err != nil {
		return DatasetManifest{}, fmt.Errorf("decode dataset manifest %q: %w", path, err)
	}
	var trailing any
	if err := decoder.Decode(&trailing); !errors.Is(err, io.EOF) {
		if err == nil {
			return DatasetManifest{}, errors.New("dataset manifest contains multiple JSON values")
		}
		return DatasetManifest{}, fmt.Errorf("decode dataset manifest trailing data: %w", err)
	}
	return manifest, nil
}
