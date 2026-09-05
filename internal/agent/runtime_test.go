package agent

import (
	"context"
	"errors"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	appclock "github.com/damirm/lazytrade/internal/clock"
	"github.com/damirm/lazytrade/internal/domain"
	"github.com/damirm/lazytrade/internal/exchange"
	"github.com/damirm/lazytrade/internal/exchange/fake"
	riskmanager "github.com/damirm/lazytrade/internal/risk"
	"github.com/damirm/lazytrade/internal/storage"
	"github.com/damirm/lazytrade/internal/storage/sqlite"
	"github.com/damirm/lazytrade/internal/strategy"
	"github.com/damirm/lazytrade/internal/strategy/movingaverage"
	"github.com/shopspring/decimal"
)

type recordingRisk struct {
	mu       sync.Mutex
	signals  []domain.Signal
	decision RiskDecision
}

type failingReconciler struct{}

func (failingReconciler) Reconcile(context.Context, domain.ExchangeAccountID) (ReconciliationReport, error) {
	return ReconciliationReport{Issues: []ReconciliationIssue{{Kind: "test"}}}, ErrReconciliationMismatch
}

func (r *recordingRisk) Evaluate(_ context.Context, signal domain.Signal) (RiskDecision, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.signals = append(r.signals, signal)
	return r.decision, nil
}

func (r *recordingRisk) count() int {
	r.mu.Lock()
	defer r.mu.Unlock()
	return len(r.signals)
}

func TestRuntimePersistsStateAndIntentBeforePlacingOrder(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	store, err := sqlite.Open(ctx, filepath.Join(t.TempDir(), "agent.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	started := time.Date(2025, 1, 1, 9, 0, 0, 0, time.UTC)
	definition := storage.StrategyDefinition{
		ID: "ma", ExchangeAccountID: "fake", InstrumentID: "TEST",
		StrategyType: movingaverage.Type, ConfigHash: "config-hash",
		CreatedAt: started, UpdatedAt: started,
	}
	if err := store.RegisterStrategy(ctx, definition); err != nil {
		t.Fatal(err)
	}
	port, err := NewPersistentStatePort(store, movingaverage.Type)
	if err != nil {
		t.Fatal(err)
	}
	implementation, err := movingaverage.New(movingaverage.Config{
		FastPeriod: 1, SlowPeriod: 2, Interval: time.Minute,
		Quantity: domain.Quantity{Value: decimal.NewFromInt(1)},
	})
	if err != nil {
		t.Fatal(err)
	}
	worker, err := strategy.NewWorker("ma", "fake", "TEST", implementation, port)
	if err != nil {
		t.Fatal(err)
	}
	adapter := fake.New("fake", exchange.Capabilities{StreamingCandles: true, Sandbox: true})
	riskGate := &recordingRisk{decision: RiskDecision{Allowed: true}}
	ready := make(chan struct{}, 1)
	orders := make(chan domain.Order, 1)
	runCtx, cancel := context.WithCancel(ctx)
	done := make(chan error, 1)
	go func() {
		done <- (Runtime{
			Exchange: adapter, Strategies: singleTestStrategy(worker, riskGate, exchange.Subscription{
				InstrumentID: "TEST", Kind: exchange.SubscriptionCandles, Interval: time.Minute,
			}, nil), Intents: store,
			Ready: ready, OnOrder: func(order domain.Order) { orders <- order },
		}).Run(runCtx)
	}()
	<-ready
	events := []domain.MarketEvent{
		agentCandle(t, started, 10, 1),
		agentCandle(t, started.Add(time.Minute), 11, 2),
		agentCandle(t, started.Add(2*time.Minute), 12, 3),
		agentCandle(t, started.Add(3*time.Minute), 8, 4),
	}
	for _, event := range events {
		if err := adapter.PublishMarket(event); err != nil {
			t.Fatal(err)
		}
	}
	var order domain.Order
	select {
	case order = <-orders:
	case <-time.After(2 * time.Second):
		t.Fatal("agent did not place an order")
	}
	cancel()
	if err := <-done; !errors.Is(err, context.Canceled) {
		t.Fatalf("Run() error = %v", err)
	}
	if riskGate.count() != 1 {
		t.Fatalf("risk evaluations = %d", riskGate.count())
	}
	intent, err := store.GetOrderIntentByClientOrderID(ctx, order.ClientOrderID)
	if err != nil {
		t.Fatal(err)
	}
	if intent.Status != "submitted" || intent.SignalID == "" || intent.Side != domain.OrderSideSell {
		t.Fatalf("intent = %#v", intent)
	}
	audit, err := store.ListAudit(ctx, 10)
	if err != nil {
		t.Fatal(err)
	}
	if len(audit) != 3 || audit[0].EventType != "order_intent_created" ||
		audit[1].EventType != "order_intent_submitting" ||
		audit[2].EventType != "order_intent_submitted" {
		t.Fatalf("audit = %#v", audit)
	}
	runtimeState, err := store.LoadRuntime(ctx, "ma")
	if err != nil {
		t.Fatal(err)
	}
	if runtimeState.Revision != 4 || runtimeState.EventCursor != strategy.CursorForEvent(events[3]) {
		t.Fatalf("runtime state = %#v", runtimeState)
	}

	restartedPort, _ := NewPersistentStatePort(store, movingaverage.Type)
	restartedWorker, _ := strategy.NewWorker("ma", "fake", "TEST", implementation, restartedPort)
	signals, err := restartedWorker.Process(ctx, events[3])
	if err != nil || len(signals) != 0 {
		t.Fatalf("duplicate event after restart: signals=%#v error=%v", signals, err)
	}
}

func TestRuntimeRoutesTwoStrategiesByInstrumentAndRisk(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	store, err := sqlite.Open(ctx, filepath.Join(t.TempDir(), "multi-agent.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	started := time.Date(2025, 1, 1, 9, 0, 0, 0, time.UTC)
	port, err := NewPersistentStatePort(store, movingaverage.Type)
	if err != nil {
		t.Fatal(err)
	}
	workers := make(map[domain.InstrumentID]*strategy.Worker, 2)
	risks := make(map[domain.StrategyID]SignalRisk, 2)
	for _, item := range []struct {
		strategyID domain.StrategyID
		instrument domain.InstrumentID
	}{
		{"ma-a", "TEST-A"},
		{"ma-b", "TEST-B"},
	} {
		if err := store.RegisterStrategy(ctx, storage.StrategyDefinition{
			ID: item.strategyID, ExchangeAccountID: "fake", InstrumentID: item.instrument,
			StrategyType: movingaverage.Type, ConfigHash: "config-" + string(item.strategyID),
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
			item.strategyID, "fake", item.instrument, implementation, port,
		)
		if err != nil {
			t.Fatal(err)
		}
		risks[item.strategyID] = &recordingRisk{decision: RiskDecision{Allowed: true}}
	}
	adapter := fake.New("fake", exchange.Capabilities{StreamingCandles: true, Sandbox: true})
	runtime := Runtime{Exchange: adapter, Intents: store}
	for index, close := range []int64{10, 11, 12, 8} {
		for _, instrument := range []domain.InstrumentID{"TEST-A", "TEST-B"} {
			event := agentCandle(t, started.Add(time.Duration(index)*time.Minute), close, uint64(index+1))
			event.InstrumentID = instrument
			if err := runtime.processEventWith(ctx, event, workers, risks); err != nil {
				t.Fatal(err)
			}
		}
	}
	for strategyID, riskGate := range risks {
		if count := riskGate.(*recordingRisk).count(); count != 1 {
			t.Fatalf("strategy %s risk evaluations = %d", strategyID, count)
		}
	}
}

func TestRuntimeRecoversSignalCommittedBeforeRiskDecision(t *testing.T) {
	t.Parallel()
	store, worker, pending := seedPendingSignal(t)
	adapter := fake.New("fake", exchange.Capabilities{StreamingCandles: true, Sandbox: true})
	ready := make(chan struct{}, 1)
	orders := make(chan domain.Order, 1)
	runCtx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() {
		done <- (Runtime{
			Exchange: adapter, Strategies: singleTestStrategy(
				worker, &recordingRisk{decision: RiskDecision{Allowed: true}}, exchange.Subscription{
					InstrumentID: "TEST", Kind: exchange.SubscriptionCandles, Interval: time.Minute,
				}, nil), Intents: store,
			Ready: ready, OnOrder: func(order domain.Order) { orders <- order },
		}).Run(runCtx)
	}()
	<-ready
	var order domain.Order
	select {
	case order = <-orders:
	case <-time.After(2 * time.Second):
		t.Fatal("pending signal was not recovered")
	}
	cancel()
	if err := <-done; !errors.Is(err, context.Canceled) {
		t.Fatalf("Run() error = %v", err)
	}
	if order.ClientOrderID != deterministicClientOrderID(pending.ID) {
		t.Fatalf("client order ID = %s", order.ClientOrderID)
	}
	if signals, err := store.ListSignalsPendingRisk(context.Background(), 10); err != nil || len(signals) != 0 {
		t.Fatalf("pending signals = %#v, error = %v", signals, err)
	}
	if _, err := store.GetOrderIntentByClientOrderID(context.Background(), order.ClientOrderID); err != nil {
		t.Fatal(err)
	}
}

func TestRuntimePersistsRejectedRecoveryWithoutIntent(t *testing.T) {
	t.Parallel()
	store, worker, pending := seedPendingSignal(t)
	adapter := fake.New("fake", exchange.Capabilities{StreamingCandles: true, Sandbox: true})
	ready := make(chan struct{}, 1)
	runCtx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() {
		done <- (Runtime{
			Exchange: adapter, Strategies: singleTestStrategy(worker, &recordingRisk{decision: RiskDecision{
				Allowed: false, ReasonCode: "test_limit", Reason: "rejected by test",
			}}, exchange.Subscription{
				InstrumentID: "TEST", Kind: exchange.SubscriptionCandles, Interval: time.Minute,
			}, nil), Intents: store,
			Ready: ready,
		}).Run(runCtx)
	}()
	<-ready
	cancel()
	if err := <-done; !errors.Is(err, context.Canceled) {
		t.Fatalf("Run() error = %v", err)
	}
	if signals, err := store.ListSignalsPendingRisk(context.Background(), 10); err != nil || len(signals) != 0 {
		t.Fatalf("pending signals = %#v, error = %v", signals, err)
	}
	if _, err := store.GetOrderIntentByClientOrderID(context.Background(), deterministicClientOrderID(pending.ID)); !errors.Is(err, storage.ErrNotFound) {
		t.Fatalf("intent error = %v", err)
	}
	audit, err := store.ListAudit(context.Background(), 10)
	if err != nil || len(audit) != 1 || audit[0].EventType != "risk_decision" {
		t.Fatalf("audit = %#v, error = %v", audit, err)
	}
}

func TestRuntimeRecoversUnknownOutcomeByClientOrderIDWithoutResubmit(t *testing.T) {
	t.Parallel()
	store, worker, pending := seedPendingSignal(t)
	adapter := fake.New("fake", exchange.Capabilities{StreamingCandles: true, Sandbox: true})
	adapter.Enqueue(fake.Scenario{Kind: fake.OrderUnknownOutcome})
	subscription := exchange.Subscription{
		InstrumentID: "TEST", Kind: exchange.SubscriptionCandles, Interval: time.Minute,
	}
	firstErr := (Runtime{
		Exchange: adapter, Strategies: singleTestStrategy(
			worker, &recordingRisk{decision: RiskDecision{Allowed: true}}, subscription, nil,
		), Intents: store,
	}).Run(context.Background())
	if firstErr == nil || !strings.Contains(firstErr.Error(), "unknown outcome") {
		t.Fatalf("first Run() error = %v", firstErr)
	}
	clientID := deterministicClientOrderID(pending.ID)
	intent, err := store.GetOrderIntentByClientOrderID(context.Background(), clientID)
	if err != nil {
		t.Fatal(err)
	}
	if intent.Status != "unknown" {
		t.Fatalf("intent after unknown outcome = %#v", intent)
	}

	ready := make(chan struct{}, 1)
	orders := make(chan domain.Order, 1)
	runCtx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() {
		done <- (Runtime{
			Exchange: adapter, Strategies: singleTestStrategy(
				worker, &recordingRisk{decision: RiskDecision{Allowed: true}}, subscription, nil,
			), Intents: store, Ready: ready,
			OnOrder: func(order domain.Order) { orders <- order },
		}).Run(runCtx)
	}()
	<-ready
	select {
	case order := <-orders:
		if order.ClientOrderID != clientID {
			t.Fatalf("recovered order client ID = %s", order.ClientOrderID)
		}
	default:
		t.Fatal("unknown order was not recovered")
	}
	cancel()
	if err := <-done; !errors.Is(err, context.Canceled) {
		t.Fatalf("second Run() error = %v", err)
	}
	intent, err = store.GetOrderIntentByClientOrderID(context.Background(), clientID)
	if err != nil || intent.Status != "submitted" {
		t.Fatalf("resolved intent = %#v, error = %v", intent, err)
	}
	open, err := adapter.OpenOrders(context.Background(), "fake")
	if err != nil || len(open) != 1 {
		t.Fatalf("open orders = %#v, error = %v", open, err)
	}
}

func TestRuntimePersistsKnownExchangeRejection(t *testing.T) {
	t.Parallel()
	store, worker, pending := seedPendingSignal(t)
	adapter := fake.New("fake", exchange.Capabilities{StreamingCandles: true, Sandbox: true})
	adapter.Enqueue(fake.Scenario{Kind: fake.OrderReject})
	ready := make(chan struct{}, 1)
	runCtx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() {
		done <- (Runtime{
			Exchange: adapter, Strategies: singleTestStrategy(
				worker, &recordingRisk{decision: RiskDecision{Allowed: true}}, exchange.Subscription{
					InstrumentID: "TEST", Kind: exchange.SubscriptionCandles, Interval: time.Minute,
				}, nil), Intents: store,
			Ready: ready,
		}).Run(runCtx)
	}()
	<-ready
	cancel()
	if err := <-done; !errors.Is(err, context.Canceled) {
		t.Fatalf("Run() error = %v", err)
	}
	intent, err := store.GetOrderIntentByClientOrderID(context.Background(), deterministicClientOrderID(pending.ID))
	if err != nil || intent.Status != "rejected" {
		t.Fatalf("rejected intent = %#v, error = %v", intent, err)
	}
}

func TestRuntimeStopsBeforeMarketSubscriptionOnReconciliationMismatch(t *testing.T) {
	t.Parallel()
	store, worker, _ := seedPendingSignal(t)
	adapter := fake.New("fake", exchange.Capabilities{StreamingCandles: true, Sandbox: true})
	firstCtx, firstCancel := context.WithCancel(context.Background())
	ready := make(chan struct{}, 1)
	done := make(chan error, 1)
	go func() {
		done <- (Runtime{
			Exchange: adapter, Strategies: singleTestStrategy(
				worker, &recordingRisk{decision: RiskDecision{Allowed: false, ReasonCode: "test"}}, exchange.Subscription{
					InstrumentID: "TEST", Kind: exchange.SubscriptionCandles, Interval: time.Minute,
				}, nil), Intents: store,
			Ready: ready,
		}).Run(firstCtx)
	}()
	<-ready
	firstCancel()
	if err := <-done; !errors.Is(err, context.Canceled) {
		t.Fatalf("setup Run() error = %v", err)
	}

	runCtx, cancel := context.WithCancel(context.Background())
	err := (Runtime{
		Exchange: adapter, Strategies: singleTestStrategy(
			worker, &recordingRisk{decision: RiskDecision{Allowed: true}}, exchange.Subscription{
				InstrumentID: "TEST", Kind: exchange.SubscriptionCandles, Interval: time.Minute,
			}, nil), Intents: store, Reconciler: failingReconciler{},
	}).Run(runCtx)
	cancel()
	if !errors.Is(err, ErrReconciliationMismatch) {
		t.Fatalf("Run() error = %v", err)
	}
}

func TestRuntimeDeduplicatesExecutionAndUpdatesPosition(t *testing.T) {
	t.Parallel()
	store, worker, _ := seedPendingSignalValues(t, []int64{12, 11, 10, 14})
	adapter := fake.New("fake", exchange.Capabilities{StreamingCandles: true, Sandbox: true})
	executedAt := time.Date(2026, 1, 1, 0, 0, 2, 0, time.UTC)
	fill := domain.Execution{
		ID: "fill-1", OrderID: "fake-order-1", StrategyID: "ma", InstrumentID: "TEST",
		Side: domain.OrderSideBuy, Quantity: domain.Quantity{Value: decimal.NewFromInt(1)},
		Price:      domain.Price{Value: decimal.NewFromInt(100), Asset: "USD"},
		Commission: domain.Money{Amount: decimal.Zero, Asset: "USD"},
		ExecutedAt: executedAt, ExchangeTrade: "trade-1",
	}
	adapter.Enqueue(fake.Scenario{Kind: fake.OrderDuplicateFill, Fills: []domain.Execution{fill}})
	ready := make(chan struct{}, 1)
	runCtx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() {
		done <- (Runtime{
			Exchange: adapter, Strategies: singleTestStrategy(
				worker, &recordingRisk{decision: RiskDecision{Allowed: true}}, exchange.Subscription{
					InstrumentID: "TEST", Kind: exchange.SubscriptionCandles, Interval: time.Minute,
				}, nil), Intents: store,
			Ready: ready, Now: func() time.Time { return executedAt.Add(time.Second) },
		}).Run(runCtx)
	}()
	<-ready
	deadline := time.Now().Add(2 * time.Second)
	var position storage.Position
	var err error
	for time.Now().Before(deadline) {
		position, err = store.LoadPosition(context.Background(), "ma", "TEST")
		if err == nil {
			break
		}
		time.Sleep(time.Millisecond)
	}
	if err != nil {
		t.Fatalf("position was not recorded: %v", err)
	}
	if position.Quantity.Value.String() != "1" || position.AveragePrice.Value.String() != "100" ||
		position.Revision != 1 {
		t.Fatalf("position = %#v", position)
	}
	daily, err := store.LoadDailyStatistics(context.Background(), "ma", "2026-01-01", "USD")
	if err != nil || daily.RealizedPnL.String() != "0" || daily.Commissions.String() != "0" ||
		daily.TradeCount != 0 {
		t.Fatalf("duplicate-adjusted daily statistics = %#v, error = %v", daily, err)
	}
	cancel()
	if err := <-done; !errors.Is(err, context.Canceled) {
		t.Fatalf("Run() error = %v", err)
	}
}

func TestRuntimePersistsRealizedPnLCommissionsAndDailyStatistics(t *testing.T) {
	t.Parallel()
	store, worker, _ := seedPendingSignalValues(t, []int64{12, 11, 10, 14})
	adapter := fake.New("fake", exchange.Capabilities{StreamingCandles: true, Sandbox: true})
	executedAt := time.Date(2026, 1, 1, 10, 0, 0, 0, time.UTC)
	execution := func(id, orderID, trade string, side domain.OrderSide, price, commission int64, at time.Time) domain.Execution {
		return domain.Execution{
			ID: domain.ExecutionID(id), OrderID: domain.OrderID(orderID),
			StrategyID: "ma", InstrumentID: "TEST", Side: side,
			Quantity:   domain.Quantity{Value: decimal.NewFromInt(1)},
			Price:      domain.Price{Value: decimal.NewFromInt(price), Asset: "USD"},
			Commission: domain.Money{Amount: decimal.NewFromInt(commission), Asset: "USD"},
			ExecutedAt: at, ExchangeTrade: trade,
		}
	}
	buy := execution("fill-buy", "fake-order-1", "trade-buy", domain.OrderSideBuy, 100, 1, executedAt)
	sell := execution("fill-sell", "fake-order-2", "trade-sell", domain.OrderSideSell, 80, 2, executedAt.Add(time.Minute))
	adapter.Enqueue(
		fake.Scenario{Kind: fake.OrderMultipleFills, Fills: []domain.Execution{buy}},
		fake.Scenario{Kind: fake.OrderMultipleFills, Fills: []domain.Execution{sell}},
	)
	dayPolicy, err := riskmanager.NewTradingDayPolicy("Europe/Moscow", "15:00")
	if err != nil {
		t.Fatal(err)
	}
	ready := make(chan struct{}, 1)
	orders := make(chan domain.Order, 2)
	runCtx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() {
		done <- (Runtime{
			Exchange: adapter, Strategies: singleTestStrategy(
				worker, &recordingRisk{decision: RiskDecision{Allowed: true}}, exchange.Subscription{
					InstrumentID: "TEST", Kind: exchange.SubscriptionCandles, Interval: time.Minute,
				}, func(at time.Time) string { return dayPolicy.At(at).Key }), Intents: store,
			Ready: ready, OnOrder: func(order domain.Order) { orders <- order },
			Now: func() time.Time { return executedAt.Add(2 * time.Minute) },
		}).Run(runCtx)
	}()
	<-ready
	started := time.Date(2025, 1, 1, 9, 0, 0, 0, time.UTC)
	if err := adapter.PublishMarket(agentCandle(t, started.Add(4*time.Minute), 9, 5)); err != nil {
		t.Fatal(err)
	}
	for index := 0; index < 2; index++ {
		select {
		case <-orders:
		case runErr := <-done:
			t.Fatalf("runtime stopped before expected orders: %v", runErr)
		case <-time.After(2 * time.Second):
			t.Fatal("expected buy and sell orders")
		}
	}
	deadline := time.Now().Add(2 * time.Second)
	var daily storage.DailyStatistics
	var loadErr error
	for time.Now().Before(deadline) {
		select {
		case runErr := <-done:
			t.Fatalf("runtime stopped before recording daily statistics: %v", runErr)
		default:
		}
		daily, loadErr = store.LoadDailyStatistics(context.Background(), "ma", "2025-12-31", "USD")
		if loadErr == nil && daily.TradeCount == 1 {
			break
		}
		time.Sleep(time.Millisecond)
	}
	if loadErr != nil {
		t.Fatalf("daily statistics were not recorded: %v", loadErr)
	}
	if daily.RealizedPnL.String() != "-20" || daily.Commissions.String() != "3" ||
		daily.TotalPnL.String() != "-23" || daily.UnrealizedPnL.String() != "0" ||
		daily.TradeCount != 1 || daily.Complete {
		t.Fatalf("daily statistics = %#v", daily)
	}
	position, err := store.LoadPosition(context.Background(), "ma", "TEST")
	if err != nil || !position.Quantity.Value.IsZero() || position.Revision != 2 {
		t.Fatalf("position = %#v, error = %v", position, err)
	}
	cancel()
	if err := <-done; !errors.Is(err, context.Canceled) {
		t.Fatalf("Run() error = %v", err)
	}

	limit := domain.Money{Amount: decimal.NewFromInt(10), Asset: "USD"}
	riskClock := appclock.NewVirtual(time.Time{})
	gate, err := NewPersistentRiskGate(riskmanager.Config{
		StrategyID: "ma", SettlementAsset: "USD", TradingDay: dayPolicy,
		MaxDailyLoss: &riskmanager.DailyLossLimit{Limit: limit, Mode: riskmanager.PnLTotal},
	}, store, riskClock)
	if err != nil {
		t.Fatal(err)
	}
	markEvent := agentCandle(t, executedAt, 80, 100)
	if err := gate.ObserveMarket(context.Background(), markEvent); err != nil {
		t.Fatal(err)
	}
	signal := domain.Signal{
		ID: "risk-signal", StrategyID: "ma", ExchangeAccountID: "fake", InstrumentID: "TEST",
		Action: domain.SignalBuy, OrderType: domain.OrderTypeMarket,
		Quantity:        domain.Quantity{Value: decimal.NewFromInt(1)},
		CreatedAt:       markEvent.ExchangeTime,
		CausativeCursor: strategy.CursorForEvent(markEvent),
	}
	decision, err := gate.Evaluate(context.Background(), signal)
	if err != nil {
		t.Fatal(err)
	}
	if decision.Allowed || decision.ReasonCode != string(riskmanager.ReasonMaxDailyLoss) ||
		gate.Status() != riskmanager.StatusRiskPaused {
		t.Fatalf("risk decision = %#v, status = %s", decision, gate.Status())
	}
}

func seedPendingSignal(t *testing.T) (*sqlite.Store, *strategy.Worker, domain.Signal) {
	return seedPendingSignalValues(t, []int64{10, 11, 12, 8})
}

func seedPendingSignalValues(t *testing.T, closes []int64) (*sqlite.Store, *strategy.Worker, domain.Signal) {
	t.Helper()
	ctx := context.Background()
	store, err := sqlite.Open(ctx, filepath.Join(t.TempDir(), "pending.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = store.Close() })
	started := time.Date(2025, 1, 1, 9, 0, 0, 0, time.UTC)
	if err := store.RegisterStrategy(ctx, storage.StrategyDefinition{
		ID: "ma", ExchangeAccountID: "fake", InstrumentID: "TEST",
		StrategyType: movingaverage.Type, ConfigHash: "config-hash",
		CreatedAt: started, UpdatedAt: started,
	}); err != nil {
		t.Fatal(err)
	}
	port, _ := NewPersistentStatePort(store, movingaverage.Type)
	implementation, _ := movingaverage.New(movingaverage.Config{
		FastPeriod: 1, SlowPeriod: 2, Interval: time.Minute,
		Quantity: domain.Quantity{Value: decimal.NewFromInt(1)},
	})
	worker, _ := strategy.NewWorker("ma", "fake", "TEST", implementation, port)
	var pending domain.Signal
	for index, closeValue := range closes {
		signals, err := worker.Process(ctx, agentCandle(t, started.Add(time.Duration(index)*time.Minute), closeValue, uint64(index+1)))
		if err != nil {
			t.Fatal(err)
		}
		if len(signals) > 0 {
			pending = signals[0]
		}
	}
	if pending.ID == "" {
		t.Fatal("fixture did not produce a signal")
	}
	return store, worker, pending
}

func agentCandle(t *testing.T, start time.Time, close int64, sequence uint64) domain.MarketEvent {
	t.Helper()
	price := func(value int64) domain.Price {
		return domain.Price{Value: decimal.NewFromInt(value), Asset: "USD"}
	}
	candle := domain.Candle{
		Start: start, End: start.Add(time.Minute), Interval: time.Minute,
		Open: price(close), High: price(close + 1), Low: price(close - 1), Close: price(close),
		Volume: domain.Quantity{Value: decimal.NewFromInt(1)}, Complete: true,
	}
	event := domain.MarketEvent{
		ExchangeAccountID: "fake", InstrumentID: "TEST",
		Kind: domain.MarketEventCandleClose, ExchangeTime: candle.End,
		ReceivedTime: candle.End, Sequence: sequence, Candle: &candle,
	}
	if err := event.Validate(); err != nil {
		t.Fatal(err)
	}
	return event
}
