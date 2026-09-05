package agent

import (
	"context"
	"errors"
	"path/filepath"
	"sync"
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

type multiRecoveryRisk struct {
	mu      sync.Mutex
	signals []domain.Signal
}

func (r *multiRecoveryRisk) Evaluate(_ context.Context, signal domain.Signal) (RiskDecision, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.signals = append(r.signals, signal)
	return RiskDecision{Allowed: true}, nil
}

func (r *multiRecoveryRisk) snapshot() []domain.Signal {
	r.mu.Lock()
	defer r.mu.Unlock()
	return append([]domain.Signal(nil), r.signals...)
}

func TestRuntimeRecoversTwoStrategiesThroughTheirOwnRiskGatesWithoutDuplicates(t *testing.T) {
	ctx := context.Background()
	store, workers, strategyIDs, subscriptions, pending := seedTwoPendingSignals(t)
	adapter := fake.New("fake", exchange.Capabilities{StreamingCandles: true, Sandbox: true})
	riskA, riskB := &multiRecoveryRisk{}, &multiRecoveryRisk{}
	risks := map[domain.StrategyID]SignalRisk{
		"ma-a": riskA,
		"ma-b": riskB,
	}

	orders := runMultiRecoveryRuntime(t, Runtime{
		Exchange: adapter, Strategies: testStrategyBindings(t, workers, strategyIDs, risks, subscriptions),
		Intents: store,
	})
	if len(orders) != 2 {
		t.Fatalf("recovered orders = %d, want 2", len(orders))
	}
	assertRecoveredByOwnRiskGate(t, riskA, "ma-a", "TEST-A", pending["ma-a"].ID)
	assertRecoveredByOwnRiskGate(t, riskB, "ma-b", "TEST-B", pending["ma-b"].ID)

	for _, order := range orders {
		expected := pending[order.StrategyID]
		if order.ClientOrderID != deterministicClientOrderID(expected.ID) {
			t.Fatalf("strategy %s client order ID = %s", order.StrategyID, order.ClientOrderID)
		}
	}
	if signals, err := store.ListSignalsPendingRisk(ctx, 10); err != nil || len(signals) != 0 {
		t.Fatalf("pending signals = %#v, error = %v", signals, err)
	}

	restartOrders := runMultiRecoveryRuntime(t, Runtime{
		Exchange: adapter, Strategies: testStrategyBindings(t, workers, strategyIDs, risks, subscriptions),
		Intents: store,
	})
	if len(restartOrders) != 0 {
		t.Fatalf("orders emitted on clean restart = %#v", restartOrders)
	}
	if got := len(riskA.snapshot()); got != 1 {
		t.Fatalf("strategy ma-a risk evaluations after restart = %d, want 1", got)
	}
	if got := len(riskB.snapshot()); got != 1 {
		t.Fatalf("strategy ma-b risk evaluations after restart = %d, want 1", got)
	}
	open, err := adapter.OpenOrders(ctx, "fake")
	if err != nil {
		t.Fatal(err)
	}
	if len(open) != 2 {
		t.Fatalf("open orders after restart = %d, want 2", len(open))
	}
}

func TestRuntimeReturnsPersistedReadyIntentsForSafeSubmission(t *testing.T) {
	ctx := context.Background()
	store, _, _, _, pending := seedTwoPendingSignals(t)
	for _, signal := range pending {
		decision, _, err := buildRiskDecision(signal, RiskDecision{Allowed: true})
		if err != nil {
			t.Fatal(err)
		}
		intent, audit, _, err := buildIntent(signal)
		if err != nil {
			t.Fatal(err)
		}
		if err := store.RecordAllowedDecisionIntent(ctx, decision, intent, audit); err != nil {
			t.Fatal(err)
		}
	}

	adapter := fake.New("fake", exchange.Capabilities{StreamingCandles: true, Sandbox: true})
	runtime := Runtime{Exchange: adapter, Intents: store}
	ready, err := runtime.resolvePendingIntents(ctx)
	if err != nil {
		t.Fatalf("resolve ready intents: %v", err)
	}
	if len(ready) != len(pending) {
		t.Fatalf("ready intents = %d, want %d", len(ready), len(pending))
	}
	open, err := adapter.OpenOrders(ctx, "fake")
	if err != nil {
		t.Fatal(err)
	}
	if len(open) != 0 {
		t.Fatalf("open orders after blocked recovery = %d, want 0", len(open))
	}
	for _, signal := range pending {
		intent, err := store.GetOrderIntentByClientOrderID(ctx, deterministicClientOrderID(signal.ID))
		if err != nil {
			t.Fatal(err)
		}
		if intent.Status != "ready" {
			t.Fatalf("intent %s status = %s, want ready", intent.ID, intent.Status)
		}
	}
}

func runMultiRecoveryRuntime(t *testing.T, runtime Runtime) []domain.Order {
	t.Helper()
	ready := make(chan struct{}, 1)
	orders := make(chan domain.Order, 4)
	runCtx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	runtime.Ready = ready
	runtime.OnOrder = func(order domain.Order) { orders <- order }
	go func() { done <- runtime.Run(runCtx) }()
	select {
	case <-ready:
	case err := <-done:
		t.Fatalf("runtime stopped before ready: %v", err)
	case <-time.After(2 * time.Second):
		t.Fatal("runtime did not become ready")
	}
	cancel()
	if err := <-done; !errors.Is(err, context.Canceled) {
		t.Fatalf("Run() error = %v", err)
	}
	close(orders)
	return append([]domain.Order(nil), drainMultiRecoveryOrders(orders)...)
}

func drainMultiRecoveryOrders(orders <-chan domain.Order) []domain.Order {
	var result []domain.Order
	for order := range orders {
		result = append(result, order)
	}
	return result
}

func assertRecoveredByOwnRiskGate(
	t *testing.T,
	risk *multiRecoveryRisk,
	strategyID domain.StrategyID,
	instrumentID domain.InstrumentID,
	signalID domain.SignalID,
) {
	t.Helper()
	signals := risk.snapshot()
	if len(signals) != 1 {
		t.Fatalf("strategy %s risk evaluations = %d, want 1", strategyID, len(signals))
	}
	if signals[0].StrategyID != strategyID || signals[0].InstrumentID != instrumentID ||
		signals[0].ID != signalID {
		t.Fatalf("strategy %s received signal %#v", strategyID, signals[0])
	}
}

func seedTwoPendingSignals(t *testing.T) (
	*sqlite.Store,
	map[domain.InstrumentID]*strategy.Worker,
	map[domain.InstrumentID]domain.StrategyID,
	[]exchange.Subscription,
	map[domain.StrategyID]domain.Signal,
) {
	t.Helper()
	ctx := context.Background()
	store, err := sqlite.Open(ctx, filepath.Join(t.TempDir(), "multi-recovery.db"))
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
	pending := make(map[domain.StrategyID]domain.Signal, 2)
	for _, fixture := range []struct {
		strategyID domain.StrategyID
		instrument domain.InstrumentID
	}{
		{"ma-a", "TEST-A"},
		{"ma-b", "TEST-B"},
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
		for index, closeValue := range []int64{12, 11, 10, 14} {
			event := agentCandle(t, started.Add(time.Duration(index)*time.Minute), closeValue, uint64(index+1))
			event.InstrumentID = fixture.instrument
			signals, err := worker.Process(ctx, event)
			if err != nil {
				t.Fatal(err)
			}
			if len(signals) > 0 {
				pending[fixture.strategyID] = signals[0]
			}
		}
		if pending[fixture.strategyID].ID == "" {
			t.Fatalf("strategy %s did not produce a pending signal", fixture.strategyID)
		}
	}
	return store, workers, strategyIDs, subscriptions, pending
}
