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

func TestRuntimeIsolatesWorkerFailureAndKeepsPeerStrategyRunning(t *testing.T) {
	ctx := context.Background()
	store, err := sqlite.Open(ctx, filepath.Join(t.TempDir(), "worker-isolation.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()

	port, err := NewPersistentStatePort(store, movingaverage.Type)
	if err != nil {
		t.Fatal(err)
	}

	started := time.Date(2026, 1, 2, 9, 0, 0, 0, time.UTC)
	workers := make(map[domain.InstrumentID]*strategy.Worker, 2)
	strategyIDs := make(map[domain.InstrumentID]domain.StrategyID, 2)
	risks := make(map[domain.StrategyID]SignalRisk, 2)
	subscriptions := make([]exchange.Subscription, 0, 2)
	for _, fixture := range []struct {
		strategyID domain.StrategyID
		instrument domain.InstrumentID
	}{
		{strategyID: "ma-a", instrument: "TEST-A"},
		{strategyID: "ma-b", instrument: "TEST-B"},
	} {
		if err := store.RegisterStrategy(ctx, storage.StrategyDefinition{
			ID:                fixture.strategyID,
			ExchangeAccountID: "fake",
			InstrumentID:      fixture.instrument,
			StrategyType:      movingaverage.Type,
			ConfigHash:        "config-" + string(fixture.strategyID),
			CreatedAt:         started,
			UpdatedAt:         started,
		}); err != nil {
			t.Fatal(err)
		}
		implementation, err := movingaverage.New(movingaverage.Config{
			FastPeriod: 1,
			SlowPeriod: 2,
			Interval:   time.Minute,
			Quantity:   domain.Quantity{Value: decimal.NewFromInt(1)},
		})
		if err != nil {
			t.Fatal(err)
		}
		workers[fixture.instrument], err = strategy.NewWorker(
			fixture.strategyID, "fake", fixture.instrument, implementation, port,
		)
		if err != nil {
			t.Fatal(err)
		}
		strategyIDs[fixture.instrument] = fixture.strategyID
		risks[fixture.strategyID] = &recordingRisk{decision: RiskDecision{Allowed: true}}
		subscriptions = append(subscriptions, exchange.Subscription{
			InstrumentID: fixture.instrument,
			Kind:         exchange.SubscriptionCandles,
			Interval:     time.Minute,
		})
	}

	// Prime only strategy A so an older event can deterministically fail its worker.
	prime := agentCandle(t, started, 10, 1)
	prime.InstrumentID = "TEST-A"
	if _, err := workers["TEST-A"].Process(ctx, prime); err != nil {
		t.Fatal(err)
	}

	adapter := fake.New("fake", exchange.Capabilities{StreamingCandles: true, Sandbox: true})
	lifecycle := newLifecycleSpy()
	ready := make(chan struct{}, 1)
	orders := make(chan domain.Order, 1)
	runCtx, cancel := context.WithCancel(ctx)
	done := make(chan error, 1)
	go func() {
		done <- (Runtime{
			Exchange:      adapter,
			Workers:       workers,
			StrategyIDs:   strategyIDs,
			Risks:         risks,
			Intents:       store,
			Lifecycle:     lifecycle,
			Subscriptions: subscriptions,
			Ready:         ready,
			OnOrder:       func(order domain.Order) { orders <- order },
			Now:           func() time.Time { return started.Add(20 * time.Minute) },
		}).Run(runCtx)
	}()

	select {
	case <-ready:
	case err := <-done:
		t.Fatalf("runtime stopped before ready: %v", err)
	case <-time.After(2 * time.Second):
		t.Fatal("runtime did not become ready")
	}

	stale := agentCandle(t, started.Add(-time.Minute), 9, 2)
	stale.InstrumentID = "TEST-A"
	if err := adapter.PublishMarket(stale); err != nil {
		t.Fatal(err)
	}
	waitForLifecycleStatus(t, lifecycle, "ma-a", RuntimeStatusFailed)

	select {
	case err := <-done:
		t.Fatalf("strategy A failure stopped account runtime: %v", err)
	default:
	}

	for index, closeValue := range []int64{12, 11, 10, 14} {
		event := agentCandle(t, started.Add(time.Duration(index)*time.Minute), closeValue, uint64(index+1))
		event.InstrumentID = "TEST-B"
		if err := adapter.PublishMarket(event); err != nil {
			t.Fatal(err)
		}
	}

	select {
	case order := <-orders:
		if order.StrategyID != "ma-b" || order.InstrumentID != "TEST-B" {
			t.Fatalf("peer order = %#v, want strategy ma-b on TEST-B", order)
		}
	case err := <-done:
		t.Fatalf("runtime stopped before peer strategy produced an order: %v", err)
	case <-time.After(2 * time.Second):
		t.Fatal("peer strategy did not continue through signal and order creation")
	}
	if count := risks["ma-b"].(*recordingRisk).count(); count != 1 {
		t.Fatalf("peer risk evaluations = %d, want 1", count)
	}

	cancel()
	if err := <-done; !errors.Is(err, context.Canceled) {
		t.Fatalf("Run() error = %v, want context cancellation", err)
	}
	requireLifecycleStatus(t, lifecycle, []domain.StrategyID{"ma-a"}, RuntimeStatusFailed)
	requireLifecycleStatus(t, lifecycle, []domain.StrategyID{"ma-b"}, RuntimeStatusStopped)
}

func TestRuntimeRecordsLateExecutionAfterOwningWorkerFails(t *testing.T) {
	ctx := context.Background()
	store, workers, strategyIDs, subscriptions := seedTwoPendingBuySignalsForIsolation(t)
	base := fake.New("fake", exchange.Capabilities{StreamingCandles: true, Sandbox: true})
	executions := make(chan domain.Execution, 1)
	streamErrors := make(chan error, 1)
	adapter := &controlledExecutionExchange{
		Exchange:   base,
		executions: executions,
		errors:     streamErrors,
	}
	lifecycle := newLifecycleSpy()
	ready := make(chan struct{}, 1)
	runCtx, cancel := context.WithCancel(ctx)
	done := make(chan error, 1)
	go func() {
		done <- (Runtime{
			Exchange:    adapter,
			Workers:     workers,
			StrategyIDs: strategyIDs,
			Risks: map[domain.StrategyID]SignalRisk{
				"ma-a": &multiRecoveryRisk{},
				"ma-b": &multiRecoveryRisk{},
			},
			Intents:       store,
			Lifecycle:     lifecycle,
			Subscriptions: subscriptions,
			Ready:         ready,
		}).Run(runCtx)
	}()

	select {
	case <-ready:
	case err := <-done:
		t.Fatalf("runtime stopped before ready: %v", err)
	case <-time.After(2 * time.Second):
		t.Fatal("runtime did not become ready")
	}

	openOrders, err := store.ListOpenOrdersByExchange(ctx, "fake")
	if err != nil {
		t.Fatal(err)
	}
	var order storage.LocalOpenOrder
	for _, candidate := range openOrders {
		if candidate.StrategyID == "ma-a" {
			order = candidate
			break
		}
	}
	if order.ExchangeOrderID == "" {
		t.Fatalf("no persisted open order for ma-a: %#v", openOrders)
	}

	stale := agentCandle(t, time.Date(2024, 12, 31, 9, 0, 0, 0, time.UTC), 10, 100)
	stale.InstrumentID = "TEST-A"
	if err := base.PublishMarket(stale); err != nil {
		t.Fatal(err)
	}
	waitForLifecycleStatus(t, lifecycle, "ma-a", RuntimeStatusFailed)

	executedAt := time.Date(2025, 1, 1, 10, 0, 0, 0, time.UTC)
	executions <- domain.Execution{
		ID:           "late-fill-a",
		OrderID:      order.ExchangeOrderID,
		StrategyID:   "ma-a",
		InstrumentID: "TEST-A",
		Side:         order.Side,
		Quantity:     order.RequestedQuantity,
		Price: domain.Price{
			Value: decimal.NewFromInt(100),
			Asset: "USD",
		},
		Commission: domain.Money{
			Amount: decimal.NewFromInt(1),
			Asset:  "USD",
		},
		ExecutedAt:    executedAt,
		ExchangeTrade: "late-trade-a",
	}

	deadline := time.Now().Add(2 * time.Second)
	for {
		select {
		case err := <-done:
			t.Fatalf("runtime stopped while recording late execution: %v", err)
		default:
		}
		position, positionErr := store.LoadPosition(ctx, "ma-a", "TEST-A")
		if positionErr == nil {
			if position.Revision != 1 {
				t.Fatalf("late execution position = %#v, want revision 1", position)
			}
			break
		}
		if !errors.Is(positionErr, storage.ErrNotFound) {
			t.Fatal(positionErr)
		}
		if time.Now().After(deadline) {
			t.Fatal("late execution for failed strategy was not recorded")
		}
		time.Sleep(time.Millisecond)
	}

	select {
	case err := <-done:
		t.Fatalf("late execution stopped account runtime: %v", err)
	default:
	}

	cancel()
	if err := <-done; !errors.Is(err, context.Canceled) {
		t.Fatalf("Run() error = %v, want context cancellation", err)
	}
	requireLifecycleStatus(t, lifecycle, []domain.StrategyID{"ma-a"}, RuntimeStatusFailed)
	requireLifecycleStatus(t, lifecycle, []domain.StrategyID{"ma-b"}, RuntimeStatusStopped)
}

func seedTwoPendingBuySignalsForIsolation(t *testing.T) (
	*sqlite.Store,
	map[domain.InstrumentID]*strategy.Worker,
	map[domain.InstrumentID]domain.StrategyID,
	[]exchange.Subscription,
) {
	t.Helper()
	ctx := context.Background()
	store, err := sqlite.Open(ctx, filepath.Join(t.TempDir(), "late-execution.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = store.Close() })
	port, err := NewPersistentStatePort(store, movingaverage.Type)
	if err != nil {
		t.Fatal(err)
	}
	started := time.Date(2025, 1, 1, 9, 0, 0, 0, time.UTC)
	workers := make(map[domain.InstrumentID]*strategy.Worker, 2)
	strategyIDs := make(map[domain.InstrumentID]domain.StrategyID, 2)
	subscriptions := make([]exchange.Subscription, 0, 2)
	for _, fixture := range []struct {
		strategyID domain.StrategyID
		instrument domain.InstrumentID
	}{
		{strategyID: "ma-a", instrument: "TEST-A"},
		{strategyID: "ma-b", instrument: "TEST-B"},
	} {
		if err := store.RegisterStrategy(ctx, storage.StrategyDefinition{
			ID: fixture.strategyID, ExchangeAccountID: "fake", InstrumentID: fixture.instrument,
			StrategyType: movingaverage.Type, ConfigHash: "config-" + string(fixture.strategyID),
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
		worker, err := strategy.NewWorker(
			fixture.strategyID, "fake", fixture.instrument, implementation, port,
		)
		if err != nil {
			t.Fatal(err)
		}
		workers[fixture.instrument] = worker
		strategyIDs[fixture.instrument] = fixture.strategyID
		subscriptions = append(subscriptions, exchange.Subscription{
			InstrumentID: fixture.instrument, Kind: exchange.SubscriptionCandles, Interval: time.Minute,
		})
		var pending bool
		for index, closeValue := range []int64{12, 11, 10, 14} {
			event := agentCandle(t, started.Add(time.Duration(index)*time.Minute), closeValue, uint64(index+1))
			event.InstrumentID = fixture.instrument
			signals, err := worker.Process(ctx, event)
			if err != nil {
				t.Fatal(err)
			}
			pending = pending || len(signals) > 0
		}
		if !pending {
			t.Fatalf("strategy %s did not produce a pending buy signal", fixture.strategyID)
		}
	}
	return store, workers, strategyIDs, subscriptions
}

func waitForLifecycleStatus(
	t *testing.T,
	lifecycle *lifecycleSpy,
	strategyID domain.StrategyID,
	status string,
) {
	t.Helper()
	deadline := time.Now().Add(2 * time.Second)
	for {
		update, ok := lifecycle.latest(strategyID)
		if ok && update.status == status {
			return
		}
		if time.Now().After(deadline) {
			t.Fatalf("strategy %s did not reach lifecycle status %s", strategyID, status)
		}
		time.Sleep(time.Millisecond)
	}
}
