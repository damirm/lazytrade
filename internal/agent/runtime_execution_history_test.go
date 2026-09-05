package agent

import (
	"context"
	"errors"
	"sync"
	"testing"
	"time"

	"github.com/damirm/lazytrade/internal/domain"
	"github.com/damirm/lazytrade/internal/exchange"
	"github.com/damirm/lazytrade/internal/exchange/fake"
	"github.com/damirm/lazytrade/internal/storage"
	"github.com/damirm/lazytrade/internal/strategy"
	"github.com/shopspring/decimal"
)

type historyExchange struct {
	exchange.Exchange

	mu       sync.Mutex
	history  exchange.ExecutionHistory
	err      error
	calls    []string
	requests []exchange.ExecutionHistoryRequest
}

func (e *historyExchange) record(call string) {
	e.mu.Lock()
	defer e.mu.Unlock()
	e.calls = append(e.calls, call)
}

func (e *historyExchange) GetOrderByClientID(ctx context.Context, id domain.ClientOrderID) (domain.Order, error) {
	e.record("lookup")
	return e.Exchange.GetOrderByClientID(ctx, id)
}

func (e *historyExchange) SubscribeExecutions(ctx context.Context, accountID domain.ExchangeAccountID) (exchange.ExecutionStream, error) {
	e.record("executions")
	return e.Exchange.SubscribeExecutions(ctx, accountID)
}

func (e *historyExchange) ExecutionHistory(_ context.Context, request exchange.ExecutionHistoryRequest) (exchange.ExecutionHistory, error) {
	e.mu.Lock()
	defer e.mu.Unlock()
	e.calls = append(e.calls, "history")
	e.requests = append(e.requests, request)
	result := e.history
	result.From, result.To = request.From, request.To
	return result, e.err
}

func (e *historyExchange) SubscribeMarketData(ctx context.Context, subscriptions []exchange.Subscription) (exchange.MarketStream, error) {
	e.record("market")
	return e.Exchange.SubscribeMarketData(ctx, subscriptions)
}

func (e *historyExchange) snapshotCalls() []string {
	e.mu.Lock()
	defer e.mu.Unlock()
	return append([]string(nil), e.calls...)
}

type projectionCheckingReconciler struct {
	store interface {
		LoadPosition(context.Context, domain.StrategyID, domain.InstrumentID) (storage.Position, error)
	}
	exchange *historyExchange
	calls    int
}

func (r *projectionCheckingReconciler) Reconcile(ctx context.Context, _ domain.ExchangeAccountID) (ReconciliationReport, error) {
	r.calls++
	position, err := r.store.LoadPosition(ctx, "ma", "TEST")
	if err != nil {
		return ReconciliationReport{}, errors.New("reconciliation ran before history projection")
	}
	if !position.Quantity.Value.Equal(decimal.NewFromInt(1)) {
		return ReconciliationReport{}, errors.New("reconciliation observed wrong recovered position")
	}
	r.exchange.record("reconcile")
	return ReconciliationReport{AccountID: "fake"}, nil
}

func TestStartupRecoversHistoryBeforeCheckpointReconciliationAndRunning(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	store, worker, intent, request, _ := seedStagedExecutionBeforeLocalOrder(t)
	base := fake.New("fake", exchange.Capabilities{StreamingCandles: true, Sandbox: true})
	remote, err := base.PlaceOrder(ctx, request)
	if err != nil {
		t.Fatal(err)
	}
	recoveryTo := time.Date(2026, 8, 2, 12, 0, 0, 0, time.UTC)
	adapter := &historyExchange{Exchange: base, history: recoveredHistory(remote, intent, recoveryTo, true)}
	reconciler := &projectionCheckingReconciler{store: store, exchange: adapter}

	ready := make(chan struct{}, 1)
	runCtx, cancel := context.WithCancel(ctx)
	done := make(chan error, 1)
	go func() {
		done <- historyRuntime(adapter, store, worker, reconciler, recoveryTo, ready).Run(runCtx)
	}()
	select {
	case <-ready:
	case err := <-done:
		t.Fatalf("runtime stopped during history recovery: %v", err)
	case <-time.After(2 * time.Second):
		t.Fatal("runtime did not complete history recovery")
	}

	assertCallOrder(t, adapter.snapshotCalls(), "lookup", "executions", "history", "reconcile", "market")
	assertIntentStatus(t, store, intent.ClientOrderID, "submitted")
	position, err := store.LoadPosition(ctx, "ma", "TEST")
	if err != nil || !position.Quantity.Value.Equal(decimal.NewFromInt(1)) || position.Revision != 1 {
		t.Fatalf("recovered position = %#v, error = %v", position, err)
	}
	daily, err := store.LoadDailyStatistics(ctx, "ma", "2026-08-02", "USD")
	if err != nil || !daily.Commissions.Equal(decimal.NewFromInt(3)) {
		t.Fatalf("recovered daily statistics = %#v, error = %v", daily, err)
	}
	checkpoint, err := store.LoadExecutionHistoryCheckpoint(ctx, "fake", "test_history")
	if err != nil || !checkpoint.CoveredThrough.Equal(recoveryTo) {
		t.Fatalf("history checkpoint = %#v, error = %v", checkpoint, err)
	}
	if reconciler.calls == 0 {
		t.Fatal("reconciliation was not called")
	}
	cancel()
	if err := <-done; !errors.Is(err, context.Canceled) {
		t.Fatalf("Run() error = %v", err)
	}
}

func TestStartupHistoryReplayIsIdempotentAcrossRestart(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	store, worker, intent, request, _ := seedStagedExecutionBeforeLocalOrder(t)
	base := fake.New("fake", exchange.Capabilities{StreamingCandles: true, Sandbox: true})
	remote, err := base.PlaceOrder(ctx, request)
	if err != nil {
		t.Fatal(err)
	}
	recoveryTo := time.Date(2026, 8, 2, 12, 0, 0, 0, time.UTC)
	adapter := &historyExchange{Exchange: base, history: recoveredHistory(remote, intent, recoveryTo, true)}

	for restart := 0; restart < 2; restart++ {
		ready := make(chan struct{}, 1)
		runCtx, cancel := context.WithCancel(ctx)
		done := make(chan error, 1)
		reconciler := &projectionCheckingReconciler{store: store, exchange: adapter}
		go func() {
			done <- historyRuntime(adapter, store, worker, reconciler, recoveryTo, ready).Run(runCtx)
		}()
		select {
		case <-ready:
		case err := <-done:
			t.Fatalf("restart %d stopped before ready: %v", restart, err)
		case <-time.After(2 * time.Second):
			t.Fatalf("restart %d did not become ready", restart)
		}
		cancel()
		if err := <-done; !errors.Is(err, context.Canceled) {
			t.Fatalf("restart %d error = %v", restart, err)
		}
	}
	position, err := store.LoadPosition(ctx, "ma", "TEST")
	if err != nil || position.Revision != 1 || !position.Quantity.Value.Equal(decimal.NewFromInt(1)) {
		t.Fatalf("position after duplicate history = %#v, error = %v", position, err)
	}
	daily, err := store.LoadDailyStatistics(ctx, "ma", "2026-08-02", "USD")
	if err != nil || !daily.Commissions.Equal(decimal.NewFromInt(3)) {
		t.Fatalf("daily after duplicate history = %#v, error = %v", daily, err)
	}
	if len(adapter.requests) != 2 {
		t.Fatalf("history requests = %d, want 2", len(adapter.requests))
	}
}

func TestStartupHistoryIncompleteOrUnattributableFailsClosed(t *testing.T) {
	t.Parallel()
	for _, test := range []struct {
		name   string
		mutate func(exchange.ExecutionHistory) exchange.ExecutionHistory
	}{
		{"incomplete", func(history exchange.ExecutionHistory) exchange.ExecutionHistory {
			history.Complete = false
			return history
		}},
		{"unattributable", func(history exchange.ExecutionHistory) exchange.ExecutionHistory {
			history.Orders[0].ClientOrderID = "unknown-client-order"
			return history
		}},
	} {
		test := test
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()
			ctx := context.Background()
			store, worker, intent, request, _ := seedStagedExecutionBeforeLocalOrder(t)
			base := fake.New("fake", exchange.Capabilities{StreamingCandles: true, Sandbox: true})
			remote, err := base.PlaceOrder(ctx, request)
			if err != nil {
				t.Fatal(err)
			}
			recoveryTo := time.Date(2026, 8, 2, 12, 0, 0, 0, time.UTC)
			history := test.mutate(recoveredHistory(remote, intent, recoveryTo, true))
			adapter := &historyExchange{Exchange: base, history: history}
			reconciler := &projectionCheckingReconciler{store: store, exchange: adapter}
			ready := make(chan struct{}, 1)

			err = historyRuntime(adapter, store, worker, reconciler, recoveryTo, ready).Run(ctx)
			if err == nil {
				t.Fatal("Run() error = nil, want fail-closed history recovery")
			}
			var blocked blockedRuntimeError
			if !errors.As(err, &blocked) {
				t.Fatalf("error type = %T (%v), want blockedRuntimeError", err, err)
			}
			select {
			case <-ready:
				t.Fatal("runtime became ready after unsafe history")
			default:
			}
			calls := adapter.snapshotCalls()
			for _, forbidden := range []string{"reconcile", "market"} {
				if containsCall(calls, forbidden) {
					t.Fatalf("calls after unsafe history = %v", calls)
				}
			}
			if _, err := store.LoadExecutionHistoryCheckpoint(ctx, "fake", "test_history"); !errors.Is(err, storage.ErrNotFound) {
				t.Fatalf("unsafe history checkpoint error = %v, want not found", err)
			}
		})
	}
}

func historyRuntime(
	adapter exchange.Exchange,
	store Store,
	worker *strategy.Worker,
	reconciler StartupReconciler,
	now time.Time,
	ready chan<- struct{},
) Runtime {
	subscription := exchange.Subscription{
		InstrumentID: "TEST", Kind: exchange.SubscriptionCandles, Interval: time.Minute,
	}
	return Runtime{
		Exchange: adapter,
		Strategies: singleTestStrategy(
			worker, &recordingRisk{decision: RiskDecision{Allowed: true}}, subscription, nil,
		),
		Intents: store,
		Ready:   ready, Now: func() time.Time { return now }, Reconciler: reconciler,
		HistorySource: "test_history", HistoryBootstrap: 48 * time.Hour, HistoryOverlap: time.Hour,
	}
}

func recoveredHistory(
	order domain.Order,
	intent storage.OrderIntent,
	to time.Time,
	complete bool,
) exchange.ExecutionHistory {
	return exchange.ExecutionHistory{
		From: to.Add(-48 * time.Hour), To: to, Complete: complete,
		Orders: []exchange.RecoveredOrderSnapshot{{
			ExchangeOrderID: order.ID, ClientOrderID: intent.ClientOrderID,
			InstrumentID: intent.InstrumentID, Side: intent.Side, Complete: true,
			OrderType: intent.OrderType, RequestedQuantity: intent.Quantity,
			Status: domain.OrderStatusFilled, SubmittedAt: order.SubmittedAt,
			CumulativeCommission: domain.Money{Amount: decimal.NewFromInt(3), Asset: "USD"},
			Fills: []exchange.RecoveredExecutionFill{{
				TradeID: "history-trade", Quantity: domain.Quantity{Value: decimal.NewFromInt(1)},
				Price:      domain.Price{Value: decimal.NewFromInt(100), Asset: "USD"},
				ExecutedAt: to.Add(-time.Minute),
			}},
		}},
	}
}

func assertCallOrder(t *testing.T, calls []string, want ...string) {
	t.Helper()
	position := -1
	for _, expected := range want {
		found := false
		for index := position + 1; index < len(calls); index++ {
			if calls[index] == expected {
				position, found = index, true
				break
			}
		}
		if !found {
			t.Fatalf("calls = %v, missing %q after index %d", calls, expected, position)
		}
	}
}

func containsCall(calls []string, want string) bool {
	for _, call := range calls {
		if call == want {
			return true
		}
	}
	return false
}
