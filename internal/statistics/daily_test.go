package statistics

import (
	"errors"
	"testing"
	"time"

	"github.com/damirm/lazytrade/internal/domain"
	"github.com/shopspring/decimal"
)

func TestDailySnapshotUsesCumulativeBaseline(t *testing.T) {
	baseline, _ := NewComponents("strategy-a", "RUB")
	baseline.Realized = decimal.NewFromInt(20)
	startEquity, _ := domain.NewMoney("1000", "RUB")
	snapshot, err := NewDailySnapshot("2026-01-01", time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC), startEquity, baseline)
	if err != nil {
		t.Fatal(err)
	}
	current := baseline
	current.Realized = decimal.NewFromInt(15)
	equity, _ := domain.NewMoney("995", "RUB")
	next, daily, err := snapshot.WithCurrent(equity, current)
	if err != nil {
		t.Fatal(err)
	}
	if got := daily.Realized.String(); got != "-5" {
		t.Fatalf("daily realized = %s, want -5", got)
	}
	if got := next.CurrentEquity.Amount.String(); got != "995" {
		t.Fatalf("current equity = %s", got)
	}
}

func TestDailySnapshotRejectsAssetMismatch(t *testing.T) {
	baseline, _ := NewComponents("strategy-a", "RUB")
	equity, _ := domain.NewMoney("1000", "USD")
	_, err := NewDailySnapshot("2026-01-01", time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC), equity, baseline)
	if !errors.Is(err, domain.ErrAssetMismatch) {
		t.Fatalf("error = %v, want asset mismatch", err)
	}
}
