package app

import (
	"context"
	"encoding/csv"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sort"
	"time"

	"github.com/damirm/lazytrade/internal/backtest"
	"github.com/damirm/lazytrade/internal/domain"
	"github.com/damirm/lazytrade/internal/exchange"
)

const DatasetManifestSchemaVersion = 1

type DownloadDataOptions struct {
	Source       exchange.HistoricalMarketData
	ExchangeID   string
	InstrumentID domain.InstrumentID
	Interval     time.Duration
	From         time.Time
	To           time.Time
	Output       string
}

type DatasetManifest struct {
	SchemaVersion uint32 `json:"schema_version"`
	Source        string `json:"source"`
	Exchange      string `json:"exchange"`
	RequestedID   string `json:"requested_instrument_id"`
	InstrumentID  string `json:"instrument_id"`
	Symbol        string `json:"symbol"`
	From          string `json:"from"`
	To            string `json:"to"`
	Interval      string `json:"interval"`
	PriceAsset    string `json:"price_asset"`
	TickSize      string `json:"tick_size"`
	LotSize       string `json:"lot_size"`
	Timezone      string `json:"timezone"`
	CandleCount   int    `json:"candle_count"`
	DatasetSHA256 string `json:"dataset_sha256"`
	DatasetFile   string `json:"dataset_file"`
	OnlyComplete  bool   `json:"only_complete_candles"`
}

type DownloadDataResult struct {
	DatasetPath  string
	ManifestPath string
	Manifest     DatasetManifest
}

func DownloadCandleData(ctx context.Context, options DownloadDataOptions) (DownloadDataResult, error) {
	if options.Source == nil {
		return DownloadDataResult{}, errors.New("historical data source is required")
	}
	if err := options.InstrumentID.Validate(); err != nil {
		return DownloadDataResult{}, fmt.Errorf("instrument: %w", err)
	}
	if options.Interval <= 0 || options.From.IsZero() || options.To.IsZero() || !options.From.Before(options.To) {
		return DownloadDataResult{}, errors.New("positive interval and a valid from/to range are required")
	}
	if options.Output == "" {
		return DownloadDataResult{}, errors.New("output path is required")
	}
	instrument, err := options.Source.Instrument(ctx, options.InstrumentID)
	if err != nil {
		return DownloadDataResult{}, fmt.Errorf("resolve instrument: %w", err)
	}
	candles, err := downloadCandles(ctx, options.Source, instrument, options)
	if err != nil {
		return DownloadDataResult{}, err
	}
	if len(candles) == 0 {
		return DownloadDataResult{}, errors.New("source returned no complete candles for the requested range")
	}
	output, err := filepath.Abs(options.Output)
	if err != nil {
		return DownloadDataResult{}, fmt.Errorf("resolve output: %w", err)
	}
	if err := os.MkdirAll(filepath.Dir(output), 0o750); err != nil {
		return DownloadDataResult{}, fmt.Errorf("create dataset directory: %w", err)
	}
	if err := writeAtomic(output, func(writer io.Writer) error { return writeCandlesCSV(writer, candles) }); err != nil {
		return DownloadDataResult{}, fmt.Errorf("write dataset: %w", err)
	}
	complete := false
	manifestPath := output + ".metadata.json"
	defer func() {
		if !complete {
			_ = os.Remove(output)
			_ = os.Remove(manifestPath)
		}
	}()
	checksum, err := hashFile(output)
	if err != nil {
		return DownloadDataResult{}, fmt.Errorf("checksum dataset: %w", err)
	}
	if err := validateDownloadedDataset(ctx, output, checksum, instrument, options.Interval, options.ExchangeID); err != nil {
		return DownloadDataResult{}, fmt.Errorf("validate downloaded dataset: %w", err)
	}
	manifest := DatasetManifest{
		SchemaVersion: DatasetManifestSchemaVersion, Source: "tinvest", Exchange: options.ExchangeID,
		RequestedID: string(options.InstrumentID), InstrumentID: string(instrument.ID), Symbol: instrument.Symbol,
		From: options.From.UTC().Format(time.RFC3339), To: options.To.UTC().Format(time.RFC3339),
		Interval: options.Interval.String(), PriceAsset: instrument.QuoteAsset,
		TickSize: instrument.PriceStep.Value.String(), LotSize: instrument.QuantityStep.Value.String(),
		Timezone: "UTC", CandleCount: len(candles), DatasetSHA256: checksum,
		DatasetFile: filepath.Base(output), OnlyComplete: true,
	}
	payload, err := json.MarshalIndent(manifest, "", "  ")
	if err != nil {
		return DownloadDataResult{}, err
	}
	payload = append(payload, '\n')
	if err := writeAtomic(manifestPath, func(writer io.Writer) error {
		_, err := writer.Write(payload)
		return err
	}); err != nil {
		return DownloadDataResult{}, fmt.Errorf("write dataset manifest: %w", err)
	}
	complete = true
	return DownloadDataResult{DatasetPath: output, ManifestPath: manifestPath, Manifest: manifest}, nil
}

func downloadCandles(ctx context.Context, source exchange.HistoricalMarketData, instrument domain.Instrument, options DownloadDataOptions) ([]domain.Candle, error) {
	chunk := historyChunk(options.Interval)
	byTime := make(map[int64]domain.Candle)
	for from := options.From.UTC(); from.Before(options.To); {
		to := from.Add(chunk)
		if to.After(options.To) {
			to = options.To
		}
		items, err := source.Candles(ctx, exchange.CandleQuery{
			InstrumentID: options.InstrumentID, Asset: instrument.QuoteAsset,
			From: from, To: to, Interval: options.Interval, Limit: 2400,
		})
		if err != nil {
			return nil, fmt.Errorf("download candles [%s, %s): %w", from.Format(time.RFC3339), to.Format(time.RFC3339), err)
		}
		for _, candle := range items {
			start := candle.Start.UTC()
			if start.Before(options.From) || !start.Before(options.To) || !candle.Complete {
				continue
			}
			if candle.Interval != options.Interval {
				return nil, fmt.Errorf("source returned candle with interval %s, expected %s", candle.Interval, options.Interval)
			}
			if err := candle.Validate(); err != nil {
				return nil, fmt.Errorf("source returned invalid candle at %s: %w", start.Format(time.RFC3339), err)
			}
			if candle.Open.Asset != instrument.QuoteAsset {
				return nil, fmt.Errorf("source returned candle asset %q, expected %q", candle.Open.Asset, instrument.QuoteAsset)
			}
			// T-Invest candle volume is expressed in lots. Dataset volume is
			// normalized to instrument units so lot-size validation is uniform.
			candle.Volume.Value = candle.Volume.Value.Mul(instrument.QuantityStep.Value)
			byTime[start.UnixNano()] = candle
		}
		from = to
	}
	result := make([]domain.Candle, 0, len(byTime))
	for _, candle := range byTime {
		result = append(result, candle)
	}
	sort.Slice(result, func(i, j int) bool { return result[i].Start.Before(result[j].Start) })
	return result, nil
}

func validateDownloadedDataset(ctx context.Context, path, checksum string, instrument domain.Instrument, interval time.Duration, exchangeID string) error {
	input, err := os.Open(path)
	if err != nil {
		return err
	}
	defer input.Close()
	iterator, err := backtest.NewCSVIterator(input, backtest.DatasetMetadata{
		Version: 1, ExchangeAccountID: domain.ExchangeAccountID(exchangeID),
		InstrumentID: instrument.ID, Interval: interval, PriceAsset: instrument.QuoteAsset,
		Timezone: time.UTC, TimestampLayout: time.RFC3339Nano,
		TickSize: instrument.PriceStep, LotSize: instrument.QuantityStep,
		GapPolicy: backtest.GapMark, ExpectedSHA256: checksum,
	})
	if err != nil {
		return err
	}
	for {
		if _, err := iterator.Next(ctx); errors.Is(err, io.EOF) {
			return nil
		} else if err != nil {
			return err
		}
	}
}

func historyChunk(interval time.Duration) time.Duration {
	switch interval {
	case time.Minute:
		return 24 * time.Hour
	case 5 * time.Minute, 10 * time.Minute:
		return 7 * 24 * time.Hour
	case 15 * time.Minute, 30 * time.Minute:
		return 21 * 24 * time.Hour
	case time.Hour, 2 * time.Hour, 4 * time.Hour:
		return 90 * 24 * time.Hour
	case 24 * time.Hour:
		return 365 * 24 * time.Hour
	default:
		return 2400 * interval
	}
}

func writeCandlesCSV(output io.Writer, candles []domain.Candle) error {
	writer := csv.NewWriter(output)
	if err := writer.Write([]string{"timestamp", "open", "high", "low", "close", "volume"}); err != nil {
		return err
	}
	for _, candle := range candles {
		if err := writer.Write([]string{
			candle.Start.UTC().Format(time.RFC3339Nano), candle.Open.Value.String(),
			candle.High.Value.String(), candle.Low.Value.String(), candle.Close.Value.String(),
			candle.Volume.Value.String(),
		}); err != nil {
			return err
		}
	}
	writer.Flush()
	return writer.Error()
}
