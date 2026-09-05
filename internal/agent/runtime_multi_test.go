package agent

import (
	"context"
	"errors"
	"path/filepath"
	"testing"
	"time"

	"github.com/damirm/lazytrade/internal/domain"
	"github.com/damirm/lazytrade/internal/exchange"
	"github.com/damirm/lazytrade/internal/exchange/fake"
	"github.com/damirm/lazytrade/internal/storage"
	"github.com/damirm/lazytrade/internal/storage/sqlite"
	"github.com/damirm/lazytrade/internal/strategy"
	"github.com/damirm/lazytrade/internal/strategy/movingaverage"
	"github.com/shopspring/decimal"
)

func TestRuntimeRunsTwoStrategiesThroughSharedStreams(t *testing.T) {
	t.Parallel()

	ctx := context.Background()
	store, err := sqlite.Open(ctx, filepath.Join(t.TempDir(), "multi-runtime.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()

	started := time.Date(2026, 1, 2, 9, 0, 0, 0, time.UTC)
	strategies := []struct {
		id         domain.StrategyID
		instrument domain.InstrumentID
		price      int64
		commission int64
	}{
		{id: "strategy-a", instrument: "TEST-A", price: 101, commission: 1},
		{id: "strategy-b", instrument: "TEST-B", price: 202, commission: 2},
	}

	port, err := NewPersistentStatePort(store, movingaverage.Type)
	if err != nil {
		t.Fatal(err)
	}
	workers := make(map[domain.InstrumentID]*strategy.Worker, len(strategies))
	strategyIDs := make(map[domain.InstrumentID]domain.StrategyID, len(strategies))
	risks := make(map[domain.StrategyID]SignalRisk, len(strategies))
	subscriptions := make([]exchange.Subscription, 0, len(strategies))
	for _, item := range strategies {
		if err := store.RegisterStrategy(ctx, storage.StrategyDefinition{
			ID: item.id, ExchangeAccountID: "fake", InstrumentID: item.instrument,
			StrategyType: movingaverage.Type, ConfigHash: "config-" + string(item.id),
			CreatedAt: started, UpdatedAt: started,
		}); err != nil {
			t.Fatal(err)
		}
		implementation, err := movingaverage.New(movingaverage.Config{
			FastPeriod: 1, SlowPeriod: 2, Interval: time.Minute,
			Quantity: domain.Quantity{Value: decimal.NewFromInt(1)},
		})
		if err != nil {
			t.Fatal(err)
		}
		workers[item.instrument], err = strategy.NewWorker(
			item.id, "fake", item.instrument, implementation, port,
		)
		if err != nil {
			t.Fatal(err)
		}
		strategyIDs[item.instrument] = item.id
		risks[item.id] = &recordingRisk{decision: RiskDecision{Allowed: true}}
		subscriptions = append(subscriptions, exchange.Subscription{
			InstrumentID: item.instrument, Kind: exchange.SubscriptionCandles, Interval: time.Minute,
		})
	}

	adapter := fake.New("fake", exchange.Capabilities{StreamingCandles: true, Sandbox: true})
	for index, item := range strategies {
		adapter.Enqueue(fake.Scenario{Kind: fake.OrderMultipleFills, Fills: []domain.Execution{{
			ID:         domain.ExecutionID("fill-" + string(item.id)),
			OrderID:    domain.OrderID("fake-order-" + decimal.NewFromInt(int64(index+1)).String()),
			StrategyID: item.id, InstrumentID: item.instrument, Side: domain.OrderSideBuy,
			Quantity:      domain.Quantity{Value: decimal.NewFromInt(1)},
			Price:         domain.Price{Value: decimal.NewFromInt(item.price), Asset: "USD"},
			Commission:    domain.Money{Amount: decimal.NewFromInt(item.commission), Asset: "USD"},
			ExecutedAt:    started.Add(10*time.Minute + time.Duration(index)*time.Second),
			ExchangeTrade: "trade-" + string(item.id),
		}}})
	}

	ready := make(chan struct{}, 1)
	orders := make(chan domain.Order, len(strategies))
	runCtx, cancel := context.WithCancel(ctx)
	done := make(chan error, 1)
	go func() {
		done <- (Runtime{
			Exchange: adapter, Workers: workers, StrategyIDs: strategyIDs, Risks: risks,
			Intents: store, Subscriptions: subscriptions, Ready: ready,
			OnOrder: func(order domain.Order) { orders <- order },
			Now:     func() time.Time { return started.Add(20 * time.Minute) },
		}).Run(runCtx)
	}()
	<-ready

	for index, close := range []int64{12, 11, 10, 14} {
		for _, item := range strategies {
			event := agentCandle(t, started.Add(time.Duration(index)*time.Minute), close, uint64(index+1))
			event.InstrumentID = item.instrument
			if err := adapter.PublishMarket(event); err != nil {
				t.Fatal(err)
			}
		}
	}

	gotOrders := make(map[domain.StrategyID]domain.Order, len(strategies))
	for len(gotOrders) < len(strategies) {
		select {
		case order := <-orders:
			gotOrders[order.StrategyID] = order
		case <-time.After(2 * time.Second):
			t.Fatalf("orders received for strategies %v", gotOrders)
		}
	}
	for _, item := range strategies {
		order := gotOrders[item.id]
		if order.InstrumentID != item.instrument || order.Side != domain.OrderSideBuy {
			t.Fatalf("order for %s = %#v", item.id, order)
		}
		if count := risks[item.id].(*recordingRisk).count(); count != 1 {
			t.Fatalf("risk evaluations for %s = %d", item.id, count)
		}
	}

	deadline := time.Now().Add(2 * time.Second)
	for {
		complete := true
		for _, item := range strategies {
			position, positionErr := store.LoadPosition(ctx, item.id, item.instrument)
			daily, dailyErr := store.LoadDailyStatistics(ctx, item.id, "2026-01-02", "USD")
			if positionErr != nil || dailyErr != nil ||
				!position.Quantity.Value.Equal(decimal.NewFromInt(1)) ||
				!position.AveragePrice.Value.Equal(decimal.NewFromInt(item.price)) ||
				!daily.Commissions.Equal(decimal.NewFromInt(item.commission)) {
				complete = false
				break
			}
		}
		if complete {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("positions and daily statistics were not recorded for both strategies")
		}
		time.Sleep(time.Millisecond)
	}

	for _, item := range strategies {
		other := strategies[0]
		if other.id == item.id {
			other = strategies[1]
		}
		if _, err := store.LoadPosition(ctx, item.id, other.instrument); !errors.Is(err, storage.ErrNotFound) {
			t.Fatalf("strategy %s unexpectedly owns position for %s: %v", item.id, other.instrument, err)
		}
		daily, err := store.LoadDailyStatistics(ctx, item.id, "2026-01-02", "USD")
		if err != nil {
			t.Fatal(err)
		}
		if !daily.RealizedPnL.IsZero() || !daily.TotalPnL.Equal(decimal.NewFromInt(-item.commission)) ||
			daily.TradeCount != 0 {
			t.Fatalf("daily statistics for %s = %#v", item.id, daily)
		}
	}

	cancel()
	if err := <-done; !errors.Is(err, context.Canceled) {
		t.Fatalf("Run() error = %v", err)
	}
}
