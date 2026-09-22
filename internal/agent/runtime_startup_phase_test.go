package agent

import (
	"context"
	"errors"
	"reflect"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/damirm/lazytrade/internal/domain"
	"github.com/damirm/lazytrade/internal/exchange"
	"github.com/damirm/lazytrade/internal/exchange/fake"
	"github.com/damirm/lazytrade/internal/storage"
)

type startupTrace struct {
	mu    sync.Mutex
	calls []string
}

func (t *startupTrace) add(call string) {
	t.mu.Lock()
	defer t.mu.Unlock()
	t.calls = append(t.calls, call)
}

func (t *startupTrace) snapshot() []string {
	t.mu.Lock()
	defer t.mu.Unlock()
	return append([]string(nil), t.calls...)
}

type startupTraceExchange struct {
	exchange.Exchange
	trace      *startupTrace
	failMarket bool
}

func (e *startupTraceExchange) GetOrderByClientID(ctx context.Context, id domain.ClientOrderID) (domain.Order, error) {
	e.trace.add("lookup")
	return e.Exchange.GetOrderByClientID(ctx, id)
}

func (e *startupTraceExchange) SubscribeExecutions(ctx context.Context, accountID domain.ExchangeAccountID) (exchange.ExecutionStream, error) {
	e.trace.add("executions")
	return e.Exchange.SubscribeExecutions(ctx, accountID)
}

func (e *startupTraceExchange) ExecutionHistory(_ context.Context, request exchange.ExecutionHistoryRequest) (exchange.ExecutionHistory, error) {
	e.trace.add("history")
	return exchange.ExecutionHistory{From: request.From, To: request.To, Complete: true}, nil
}

func (e *startupTraceExchange) PlaceOrder(ctx context.Context, order exchange.NewOrder) (domain.Order, error) {
	e.trace.add("submit_ready")
	return e.Exchange.PlaceOrder(ctx, order)
}

func (e *startupTraceExchange) SubscribeMarketData(ctx context.Context, subscriptions []exchange.Subscription) (exchange.MarketStream, error) {
	e.trace.add("market")
	if e.failMarket {
		return exchange.MarketStream{}, errors.New("market unavailable")
	}
	return e.Exchange.SubscribeMarketData(ctx, subscriptions)
}

type startupTraceStore struct {
	Store
	trace *startupTrace
}

func (s *startupTraceStore) AdvanceExecutionHistoryCheckpoint(ctx context.Context, checkpoint storage.ExecutionHistoryCheckpoint) error {
	s.trace.add("checkpoint")
	return s.Store.AdvanceExecutionHistoryCheckpoint(ctx, checkpoint)
}

type startupTraceLifecycle struct {
	trace         *startupTrace
	failRunningID domain.StrategyID
}

func (s startupTraceLifecycle) SetStrategyStatus(_ context.Context, id domain.StrategyID, status, _ string, _ time.Time) error {
	if status == RuntimeStatusRunning {
		s.trace.add("running:" + string(id))
		if id == s.failRunningID {
			return errors.New("running lifecycle unavailable")
		}
	}
	return nil
}

type startupTraceReconciler struct {
	trace *startupTrace
	calls int
}

func (r *startupTraceReconciler) Reconcile(_ context.Context, _ domain.ExchangeAccountID) (ReconciliationReport, error) {
	r.calls++
	if r.calls == 1 {
		r.trace.add("reconcile_first")
	} else {
		r.trace.add("reconcile_second")
	}
	return ReconciliationReport{}, nil
}

func startupTraceFixture(t *testing.T, failMarket bool) (Runtime, *startupTrace) {
	t.Helper()
	ctx := context.Background()
	store, workers, strategyIDs, subscriptions, pending := seedTwoPendingSignals(t)
	base := fake.New("fake", exchange.Capabilities{StreamingCandles: true, Sandbox: true})
	for _, id := range []domain.StrategyID{"ma-a", "ma-b"} {
		signal := pending[id]
		decision, _, err := buildRiskDecision(signal, RiskDecision{Allowed: true})
		if err != nil {
			t.Fatal(err)
		}
		intent, audit, request, err := buildIntent(signal)
		if err != nil {
			t.Fatal(err)
		}
		if id == "ma-a" {
			intent.Status = "submitting"
			if _, err := base.PlaceOrder(ctx, request); err != nil {
				t.Fatal(err)
			}
		}
		if err := store.RecordAllowedDecisionIntent(ctx, decision, intent, audit); err != nil {
			t.Fatal(err)
		}
	}
	trace := &startupTrace{}
	risks := map[domain.StrategyID]SignalRisk{"ma-a": &multiRecoveryRisk{}, "ma-b": &multiRecoveryRisk{}}
	return Runtime{
		Exchange:   &startupTraceExchange{Exchange: base, trace: trace, failMarket: failMarket},
		Strategies: testStrategyBindings(t, workers, strategyIDs, risks, subscriptions),
		Intents:    &startupTraceStore{Store: store, trace: trace},
		Lifecycle:  startupTraceLifecycle{trace: trace},
		Reconciler: &startupTraceReconciler{trace: trace},
		Now:        func() time.Time { return time.Date(2026, 9, 22, 12, 0, 0, 0, time.UTC) },
	}, trace
}

func TestStartupPhaseOrdersRecoveryBeforeReady(t *testing.T) {
	runtime, trace := startupTraceFixture(t, false)
	ready := make(chan struct{}, 1)
	runtime.Ready = ready
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	done := make(chan error, 1)
	go func() { done <- runtime.Run(ctx) }()
	select {
	case <-ready:
		trace.add("ready")
	case err := <-done:
		t.Fatalf("runtime stopped before Ready: %v", err)
	case <-time.After(2 * time.Second):
		t.Fatal("runtime did not become ready")
	}
	want := []string{"lookup", "executions", "history", "reconcile_first", "checkpoint", "submit_ready", "reconcile_second", "market", "running:ma-a", "running:ma-b", "ready"}
	if got := trace.snapshot(); !reflect.DeepEqual(got, want) {
		t.Fatalf("startup trace = %v, want %v", got, want)
	}
	cancel()
	if err := <-done; !errors.Is(err, context.Canceled) {
		t.Fatalf("Run() error = %v", err)
	}
}

func TestStartupPhaseFailureBeforeRunningHasNoReadyResult(t *testing.T) {
	runtime, trace := startupTraceFixture(t, true)
	ready := make(chan struct{}, 1)
	runtime.Ready = ready
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	workers, risks, subscriptions, err := runtime.components()
	if err != nil {
		t.Fatal(err)
	}
	if err := runtime.repairFailedStrategySignals(ctx, runtime.lifecycleStrategyIDs()); err != nil {
		t.Fatal(err)
	}
	if err := runtime.setLifecycle(ctx, runtime.lifecycleStrategyIDs(), RuntimeStatusReconciling, "startup"); err != nil {
		t.Fatal(err)
	}
	state, err := runtime.startup(ctx, workers, risks, subscriptions, runtime.lifecycleStrategyIDs())
	if err == nil {
		t.Fatal("startup() error = nil")
	}
	var blocked blockedRuntimeError
	if !errors.As(err, &blocked) || err.Error() != "subscribe market data: market unavailable" {
		t.Fatalf("startup() error = %T (%v), want blocked market subscription error", err, err)
	}
	if !reflect.DeepEqual(state, startupResult{}) {
		t.Fatalf("failed startup returned nonzero state: %#v", state)
	}
	select {
	case <-ready:
		t.Fatal("Ready emitted before running")
	default:
	}
	want := []string{"lookup", "executions", "history", "reconcile_first", "checkpoint", "submit_ready", "reconcile_second", "market"}
	if got := trace.snapshot(); !reflect.DeepEqual(got, want) {
		t.Fatalf("failed startup trace = %v, want %v", got, want)
	}
}

func TestStartupPhaseRunningWriteFailureHasNoReadyResult(t *testing.T) {
	runtime, trace := startupTraceFixture(t, false)
	runtime.Lifecycle = startupTraceLifecycle{trace: trace, failRunningID: "ma-b"}
	ready := make(chan struct{}, 1)
	runtime.Ready = ready
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	workers, risks, subscriptions, err := runtime.components()
	if err != nil {
		t.Fatal(err)
	}
	if err := runtime.repairFailedStrategySignals(ctx, runtime.lifecycleStrategyIDs()); err != nil {
		t.Fatal(err)
	}
	if err := runtime.setLifecycle(ctx, runtime.lifecycleStrategyIDs(), RuntimeStatusReconciling, "startup"); err != nil {
		t.Fatal(err)
	}
	state, err := runtime.startup(ctx, workers, risks, subscriptions, runtime.lifecycleStrategyIDs())
	if err == nil || !strings.Contains(err.Error(), "persist running lifecycle: strategy ma-b lifecycle running: running lifecycle unavailable") {
		t.Fatalf("startup() error = %v, want running lifecycle persistence error", err)
	}
	if !reflect.DeepEqual(state, startupResult{}) {
		t.Fatalf("failed startup returned nonzero state: %#v", state)
	}
	select {
	case <-ready:
		t.Fatal("Ready emitted after running lifecycle write failed")
	default:
	}
	want := []string{"lookup", "executions", "history", "reconcile_first", "checkpoint", "submit_ready", "reconcile_second", "market", "running:ma-a", "running:ma-b"}
	if got := trace.snapshot(); !reflect.DeepEqual(got, want) {
		t.Fatalf("failed startup trace = %v, want %v", got, want)
	}
}
