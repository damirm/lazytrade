package app

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/damirm/lazytrade/internal/backtest"
	"github.com/damirm/lazytrade/internal/domain"
)

func TestValidateCandleDataReportsGapsAndChecksum(t *testing.T) {
	t.Parallel()
	from := time.Date(2025, 1, 1, 0, 0, 0, 0, time.UTC)
	source := &historyFixture{instrument: testInstrument(), candles: []domain.Candle{
		testCandle(from, true),
		testCandle(from.Add(2*time.Minute), true),
	}}
	output := filepath.Join(t.TempDir(), "prices.csv")
	download, err := DownloadCandleData(context.Background(), DownloadDataOptions{
		Source: source, ExchangeID: "sandbox", InstrumentID: "requested-figi",
		Interval: time.Minute, From: from, To: from.Add(3 * time.Minute), Output: output,
	})
	if err != nil {
		t.Fatal(err)
	}
	result, err := ValidateCandleData(context.Background(), ValidateDataOptions{Input: output})
	if err != nil {
		t.Fatalf("ValidateCandleData() error = %v", err)
	}
	if result.Rows != 2 || len(result.Gaps) != 1 ||
		result.SHA256 != download.Manifest.DatasetSHA256 {
		t.Fatalf("result = %#v", result)
	}
	if _, err := ValidateCandleData(context.Background(), ValidateDataOptions{
		Input: output, GapPolicy: backtest.GapFail,
	}); err == nil || !strings.Contains(err.Error(), "gap of 1 candles") {
		t.Fatalf("gap failure = %v", err)
	}
}

func TestValidateCandleDataRejectsChecksumMismatch(t *testing.T) {
	t.Parallel()
	from := time.Date(2025, 1, 1, 0, 0, 0, 0, time.UTC)
	output := filepath.Join(t.TempDir(), "prices.csv")
	download, err := DownloadCandleData(context.Background(), DownloadDataOptions{
		Source:     &historyFixture{instrument: testInstrument(), candles: []domain.Candle{testCandle(from, true)}},
		ExchangeID: "sandbox", InstrumentID: "requested-figi",
		Interval: time.Minute, From: from, To: from.Add(time.Minute), Output: output,
	})
	if err != nil {
		t.Fatal(err)
	}
	manifest := download.Manifest
	manifest.DatasetSHA256 = strings.Repeat("0", 64)
	payload, _ := json.Marshal(manifest)
	if err := os.WriteFile(download.ManifestPath, payload, 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := ValidateCandleData(context.Background(), ValidateDataOptions{Input: output}); err == nil ||
		!strings.Contains(err.Error(), "checksum mismatch") {
		t.Fatalf("checksum failure = %v", err)
	}
}
