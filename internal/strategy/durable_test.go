package strategy_test

import (
	"context"
	"path/filepath"
	"testing"
	"time"

	"github.com/damirm/lazytrade/internal/domain"
	"github.com/damirm/lazytrade/internal/storage"
	"github.com/damirm/lazytrade/internal/storage/sqlite"
	"github.com/damirm/lazytrade/internal/strategy"
	"github.com/damirm/lazytrade/internal/strategy/movingaverage"
	"github.com/shopspring/decimal"
)

func TestDurableStatePortRestoresStateAndCursor(t *testing.T) {
	ctx := context.Background()
	store, err := sqlite.Open(ctx, filepath.Join(t.TempDir(), "agent.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	now := time.Date(2026, 7, 29, 10, 0, 0, 0, time.UTC)
	if err := store.RegisterStrategy(ctx, storage.StrategyDefinition{
		ID: "ma", ExchangeAccountID: "sandbox", InstrumentID: "instrument",
		StrategyType: movingaverage.Type, ConfigHash: "hash", CreatedAt: now, UpdatedAt: now,
	}); err != nil {
		t.Fatal(err)
	}
	implementation, _ := movingaverage.New(movingaverage.Config{
		FastPeriod: 1, SlowPeriod: 2, Interval: time.Minute,
		Quantity: domain.Quantity{Value: decimal.NewFromInt(1)},
	})
	port, err := strategy.NewDurableStatePort(store, movingaverage.Type)
	if err != nil {
		t.Fatal(err)
	}
	worker, _ := strategy.NewWorker("ma", "sandbox", "instrument", implementation, port)
	event := domain.MarketEvent{
		ExchangeAccountID: "sandbox", InstrumentID: "instrument",
		Kind: domain.MarketEventCandleClose, ExchangeTime: now, ReceivedTime: now, Sequence: 1,
		Candle: &domain.Candle{
			Interval: time.Minute, Start: now.Add(-time.Minute), End: now,
			Open:   domain.Price{Value: decimal.NewFromInt(100), Asset: "RUB"},
			High:   domain.Price{Value: decimal.NewFromInt(101), Asset: "RUB"},
			Low:    domain.Price{Value: decimal.NewFromInt(99), Asset: "RUB"},
			Close:  domain.Price{Value: decimal.NewFromInt(100), Asset: "RUB"},
			Volume: domain.Quantity{Value: decimal.NewFromInt(1)}, Complete: true,
		},
	}
	if _, err := worker.Process(ctx, event); err != nil {
		t.Fatal(err)
	}
	restarted, _ := strategy.NewDurableStatePort(store, movingaverage.Type)
	snapshot, exists, err := restarted.Load(ctx, "ma")
	if err != nil || !exists {
		t.Fatalf("restored state exists=%v error=%v", exists, err)
	}
	if snapshot.LastCursor == nil || snapshot.LastCursor.Sequence != 1 ||
		snapshot.State.StrategyType != movingaverage.Type {
		t.Fatalf("restored snapshot = %+v", snapshot)
	}
}
