package agent

import (
	"context"
	"errors"
	"path/filepath"
	"strings"
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

type lifecycleUpdate struct {
	strategyID domain.StrategyID
	status     string
	reason     string
}

func TestRuntimePersistsLifecycleBeforeFirstMarketEvent(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	store, err := sqlite.Open(ctx, filepath.Join(t.TempDir(), "lifecycle.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = store.Close() })
	started := time.Date(2026, 9, 6, 9, 0, 0, 0, time.UTC)
	definition := storage.StrategyDefinition{
		ID: "ma", ExchangeAccountID: "fake", InstrumentID: "TEST",
		StrategyType: movingaverage.Type, ConfigHash: "lifecycle-test",
		CreatedAt: started, UpdatedAt: started,
	}
	if err := store.RegisterStrategy(ctx, definition); err != nil {
		t.Fatal(err)
	}
	implementation, err := movingaverage.New(movingaverage.Config{
		FastPeriod: 1, SlowPeriod: 2, Interval: time.Minute,
		Quantity: domain.Quantity{Value: decimal.NewFromInt(1)},
	})
	if err != nil {
		t.Fatal(err)
	}
	port, err := NewPersistentStatePort(store, movingaverage.Type)
	if err != nil {
		t.Fatal(err)
	}
	worker, err := strategy.NewWorker("ma", "fake", "TEST", implementation, port)
	if err != nil {
		t.Fatal(err)
	}
	adapter := fake.New("fake", exchange.Capabilities{StreamingCandles: true, Sandbox: true})
	subscription := exchange.Subscription{
		InstrumentID: "TEST", Kind: exchange.SubscriptionCandles, Interval: time.Minute,
	}
	ready := make(chan struct{}, 1)
	runCtx, cancel := context.WithCancel(ctx)
	done := make(chan error, 1)
	go func() {
		done <- (Runtime{
			Exchange: adapter,
			Strategies: singleTestStrategy(
				worker, &recordingRisk{decision: RiskDecision{Allowed: true}}, subscription, nil,
			),
			Intents: store, Lifecycle: store, Ready: ready,
		}).Run(runCtx)
	}()
	select {
	case <-ready:
	case err := <-done:
		t.Fatalf("runtime stopped before ready: %v", err)
	case <-time.After(2 * time.Second):
		t.Fatal("runtime did not become ready")
	}
	lifecycle, err := store.LoadStrategyLifecycle(ctx, "ma")
	if err != nil || lifecycle.Status != RuntimeStatusRunning {
		t.Fatalf("running lifecycle=%+v error=%v", lifecycle, err)
	}
	if _, err := store.LoadRuntime(ctx, "ma"); !errors.Is(err, storage.ErrNotFound) {
		t.Fatalf("strategy state before first market event error=%v, want not found", err)
	}
	cancel()
	if err := <-done; !errors.Is(err, context.Canceled) {
		t.Fatalf("Run() error=%v", err)
	}
	lifecycle, err = store.LoadStrategyLifecycle(ctx, "ma")
	if err != nil || lifecycle.Status != RuntimeStatusStopped {
		t.Fatalf("stopped lifecycle=%+v error=%v", lifecycle, err)
	}
}

type lifecycleSpy struct {
	mu      sync.Mutex
	updates []lifecycleUpdate
	changed chan struct{}
}

func newLifecycleSpy() *lifecycleSpy {
	return &lifecycleSpy{changed: make(chan struct{}, 16)}
}

func (s *lifecycleSpy) SetStrategyStatus(
	_ context.Context,
	strategyID domain.StrategyID,
	status string,
	reason string,
	_ time.Time,
) error {
	s.mu.Lock()
	s.updates = append(s.updates, lifecycleUpdate{
		strategyID: strategyID,
		status:     status,
		reason:     reason,
	})
	s.mu.Unlock()
	select {
	case s.changed <- struct{}{}:
	default:
	}
	return nil
}

func (s *lifecycleSpy) latest(strategyID domain.StrategyID) (lifecycleUpdate, bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	for index := len(s.updates) - 1; index >= 0; index-- {
		if s.updates[index].strategyID == strategyID {
			return s.updates[index], true
		}
	}
	return lifecycleUpdate{}, false
}

func requireLifecycleStatus(
	t *testing.T,
	spy *lifecycleSpy,
	strategyIDs []domain.StrategyID,
	wantStatus string,
) {
	t.Helper()
	deadline := time.NewTimer(2 * time.Second)
	defer deadline.Stop()
	for {
		allMatch := true
		for _, strategyID := range strategyIDs {
			update, ok := spy.latest(strategyID)
			if !ok || update.status != wantStatus {
				allMatch = false
				break
			}
		}
		if allMatch {
			return
		}
		select {
		case <-spy.changed:
		case <-deadline.C:
			got := make(map[domain.StrategyID]lifecycleUpdate, len(strategyIDs))
			for _, strategyID := range strategyIDs {
				got[strategyID], _ = spy.latest(strategyID)
			}
			t.Fatalf("latest lifecycle updates = %#v, want status %q", got, wantStatus)
		}
	}
}

func TestRuntimeLifecycleIsRunningWhileReadyAndStoppedOnCancellation(t *testing.T) {
	store, workers, strategyIDs, subscriptions, _ := seedTwoPendingSignals(t)
	lifecycle := newLifecycleSpy()
	adapter := fake.New("fake", exchange.Capabilities{StreamingCandles: true, Sandbox: true})
	ready := make(chan struct{}, 1)
	runCtx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	risks := map[domain.StrategyID]SignalRisk{
		"ma-a": &multiRecoveryRisk{},
		"ma-b": &multiRecoveryRisk{},
	}
	go func() {
		done <- (Runtime{
			Exchange:   adapter,
			Strategies: testStrategyBindings(t, workers, strategyIDs, risks, subscriptions),
			Intents:    store, Lifecycle: lifecycle,
			Ready: ready,
		}).Run(runCtx)
	}()

	select {
	case <-ready:
	case err := <-done:
		t.Fatalf("runtime stopped before ready: %v", err)
	case <-time.After(2 * time.Second):
		t.Fatal("runtime did not become ready")
	}
	ids := []domain.StrategyID{"ma-a", "ma-b"}
	requireLifecycleStatus(t, lifecycle, ids, "running")

	cancel()
	if err := <-done; !errors.Is(err, context.Canceled) {
		t.Fatalf("Run() error = %v, want context cancellation", err)
	}
	requireLifecycleStatus(t, lifecycle, ids, "stopped")
	for _, strategyID := range ids {
		update, _ := lifecycle.latest(strategyID)
		if update.reason != "" {
			t.Errorf("%s stopped reason = %q, want empty", strategyID, update.reason)
		}
	}
}

func TestRuntimeLifecycleBlocksAllStrategiesOnExecutionStreamError(t *testing.T) {
	store, workers, strategyIDs, subscriptions, _ := seedTwoPendingSignals(t)
	lifecycle := newLifecycleSpy()
	executions := make(chan domain.Execution)
	streamErrors := make(chan error, 1)
	adapter := &controlledExecutionExchange{
		Exchange:   fake.New("fake", exchange.Capabilities{StreamingCandles: true, Sandbox: true}),
		executions: executions,
		errors:     streamErrors,
	}
	ready := make(chan struct{}, 1)
	done := make(chan error, 1)
	risks := map[domain.StrategyID]SignalRisk{
		"ma-a": &multiRecoveryRisk{},
		"ma-b": &multiRecoveryRisk{},
	}
	go func() {
		done <- (Runtime{
			Exchange:   adapter,
			Strategies: testStrategyBindings(t, workers, strategyIDs, risks, subscriptions),
			Intents:    store, Lifecycle: lifecycle,
			Ready: ready,
		}).Run(context.Background())
	}()

	select {
	case <-ready:
	case err := <-done:
		t.Fatalf("runtime stopped before ready: %v", err)
	case <-time.After(2 * time.Second):
		t.Fatal("runtime did not become ready")
	}
	ids := []domain.StrategyID{"ma-a", "ma-b"}
	requireLifecycleStatus(t, lifecycle, ids, "running")

	streamErrors <- errors.New("transport unavailable")
	err := <-done
	if err == nil || !strings.Contains(err.Error(), "execution stream: transport unavailable") {
		t.Fatalf("Run() error = %v", err)
	}
	requireLifecycleStatus(t, lifecycle, ids, "blocked")
	for _, strategyID := range ids {
		update, _ := lifecycle.latest(strategyID)
		if update.reason != err.Error() {
			t.Errorf("%s blocked reason = %q, want %q", strategyID, update.reason, err)
		}
	}
}
