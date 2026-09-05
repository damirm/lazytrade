package app

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/damirm/lazytrade/internal/domain"
	"github.com/damirm/lazytrade/internal/exchange"
	"github.com/shopspring/decimal"
)

type historyFixture struct {
	instrument domain.Instrument
	candles    []domain.Candle
	queries    []exchange.CandleQuery
}

func (f *historyFixture) Instrument(context.Context, domain.InstrumentID) (domain.Instrument, error) {
	return f.instrument, nil
}

func (f *historyFixture) Candles(_ context.Context, query exchange.CandleQuery) ([]domain.Candle, error) {
	f.queries = append(f.queries, query)
	return append([]domain.Candle(nil), f.candles...), nil
}

func TestDownloadCandleDataWritesValidatedDatasetAndManifest(t *testing.T) {
	t.Parallel()
	from := time.Date(2025, 1, 1, 0, 0, 0, 0, time.UTC)
	source := &historyFixture{instrument: testInstrument(), candles: []domain.Candle{
		testCandle(from.Add(time.Minute), true),
		testCandle(from, true),
		testCandle(from.Add(2*time.Minute), false),
	}}
	output := filepath.Join(t.TempDir(), "nested", "prices.csv")
	result, err := DownloadCandleData(context.Background(), DownloadDataOptions{
		Source: source, ExchangeID: "sandbox", InstrumentID: "requested-figi",
		Interval: time.Minute, From: from, To: from.Add(3 * time.Minute), Output: output,
	})
	if err != nil {
		t.Fatalf("DownloadCandleData() error = %v", err)
	}
	data, err := os.ReadFile(output)
	if err != nil {
		t.Fatal(err)
	}
	lines := strings.Split(strings.TrimSpace(string(data)), "\n")
	if len(lines) != 3 || !strings.HasPrefix(lines[1], "2025-01-01T00:00:00Z") ||
		!strings.HasSuffix(lines[1], ",20") {
		t.Fatalf("dataset = %q", data)
	}
	if result.Manifest.CandleCount != 2 || len(result.Manifest.DatasetSHA256) != 64 ||
		result.Manifest.RequestedID != "requested-figi" ||
		result.Manifest.InstrumentID != "canonical-uid" || result.Manifest.LotSize != "10" {
		t.Fatalf("manifest = %#v", result.Manifest)
	}
	manifestData, err := os.ReadFile(result.ManifestPath)
	if err != nil {
		t.Fatal(err)
	}
	var manifest DatasetManifest
	if err := json.Unmarshal(manifestData, &manifest); err != nil {
		t.Fatal(err)
	}
	if manifest != result.Manifest {
		t.Fatalf("stored manifest = %#v, result = %#v", manifest, result.Manifest)
	}
}

func TestDownloadCandleDataRejectsEmptyCompleteRange(t *testing.T) {
	t.Parallel()
	from := time.Date(2025, 1, 1, 0, 0, 0, 0, time.UTC)
	source := &historyFixture{instrument: testInstrument(), candles: []domain.Candle{testCandle(from, false)}}
	_, err := DownloadCandleData(context.Background(), DownloadDataOptions{
		Source: source, ExchangeID: "sandbox", InstrumentID: "id",
		Interval: time.Minute, From: from, To: from.Add(time.Minute), Output: filepath.Join(t.TempDir(), "data.csv"),
	})
	if err == nil || !strings.Contains(err.Error(), "no complete candles") {
		t.Fatalf("error = %v", err)
	}
}

func testInstrument() domain.Instrument {
	return domain.Instrument{
		ID: "canonical-uid", ExchangeAccount: "sandbox", Symbol: "TEST", BaseAsset: "TEST",
		QuoteAsset: "RUB", SettlementAsset: "RUB",
		PriceStep:    domain.Price{Value: decimal.NewFromFloat(0.01), Asset: "RUB"},
		QuantityStep: domain.Quantity{Value: decimal.NewFromInt(10)},
		MinQuantity:  domain.Quantity{Value: decimal.NewFromInt(10)},
	}
}

func testCandle(start time.Time, complete bool) domain.Candle {
	price := func(value int64) domain.Price {
		return domain.Price{Value: decimal.NewFromInt(value), Asset: "RUB"}
	}
	return domain.Candle{
		Start: start, End: start.Add(time.Minute), Interval: time.Minute,
		Open: price(10), High: price(12), Low: price(9), Close: price(11),
		Volume: domain.Quantity{Value: decimal.NewFromInt(2)}, Complete: complete,
	}
}
