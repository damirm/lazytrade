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
	"github.com/damirm/lazytrade/internal/storage/sqlite"
	"github.com/damirm/lazytrade/internal/strategy"
	"github.com/shopspring/decimal"
)

func TestStartupDrainsStagedExecutionAfterRecoveringLocalOrder(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	store, worker, intent, _, fill := seedStagedExecutionBeforeLocalOrder(t)
	adapter := fake.New("fake", exchange.Capabilities{StreamingCandles: true, Sandbox: true})
	remote, err := adapter.PlaceOrder(ctx, requestForIntent(intent))
	if err != nil {
		t.Fatalf("seed remote order: %v", err)
	}
	fill.OrderID = remote.ID
	if _, _, err := store.StageExecution(ctx, "fake", fill, fill.ExecutedAt.Add(time.Second), "2026-01-01"); err != nil {
		t.Fatalf("stage execution before local order: %v", err)
	}

	ready := make(chan struct{}, 1)
	runCtx, cancel := context.WithCancel(ctx)
	done := make(chan error, 1)
	go func() {
		done <- (Runtime{
			Exchange: adapter, Worker: worker,
			Risk: &recordingRisk{decision: RiskDecision{Allowed: true}}, Intents: store,
			Subscription: exchange.Subscription{
				InstrumentID: "TEST", Kind: exchange.SubscriptionCandles, Interval: time.Minute,
			},
			Ready: ready,
		}).Run(runCtx)
	}()
	select {
	case <-ready:
	case <-time.After(2 * time.Second):
		t.Fatal("runtime did not finish startup inbox drain")
	}

	assertIntentStatus(t, store, intent.ClientOrderID, "submitted")
	position, err := store.LoadPosition(ctx, "ma", "TEST")
	if err != nil {
		t.Fatalf("load recovered position: %v", err)
	}
	if !position.Quantity.Value.Equal(decimal.NewFromInt(1)) || position.Revision != 1 {
		t.Fatalf("position after startup drain = %#v", position)
	}
	pending, err := store.ListPendingExecutions(ctx, "fake", 10)
	if err != nil || len(pending) != 0 {
		t.Fatalf("pending executions after startup = %#v, error = %v", pending, err)
	}
	cancel()
	if err := <-done; !errors.Is(err, context.Canceled) {
		t.Fatalf("Run() error = %v", err)
	}
}

func TestExecutionInboxDuplicateReplayDoesNotReapplyProjections(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	store, _, intent, request, fill := seedStagedExecutionBeforeLocalOrder(t)
	adapter := fake.New("fake", exchange.Capabilities{Sandbox: true})
	remote, err := adapter.PlaceOrder(ctx, request)
	if err != nil {
		t.Fatal(err)
	}
	fill.OrderID = remote.ID
	runtime := Runtime{Exchange: adapter, Intents: store}
	if _, err := runtime.resolvePendingIntents(ctx); err != nil {
		t.Fatalf("recover local order: %v", err)
	}

	receivedAt := fill.ExecutedAt.Add(time.Second)
	if err := runtime.recordExecution(ctx, "fake", fill, receivedAt, "2026-01-01"); err != nil {
		t.Fatalf("first execution ingestion: %v", err)
	}
	if err := runtime.recordExecution(ctx, "fake", fill, receivedAt, "2026-01-01"); err != nil {
		t.Fatalf("duplicate execution ingestion: %v", err)
	}
	assertIntentStatus(t, store, intent.ClientOrderID, "submitted")
	position, err := store.LoadPosition(ctx, "ma", "TEST")
	if err != nil {
		t.Fatal(err)
	}
	if !position.Quantity.Value.Equal(decimal.NewFromInt(1)) || position.Revision != 1 {
		t.Fatalf("position after duplicate replay = %#v", position)
	}
	daily, err := store.LoadDailyStatistics(ctx, "ma", "2026-01-01", "USD")
	if err != nil || !daily.Commissions.Equal(decimal.NewFromInt(2)) {
		t.Fatalf("daily statistics after duplicate replay = %#v, error = %v", daily, err)
	}
}

type recordingExecutionInboxStore struct {
	Store
	mu         sync.Mutex
	stageCalls int
	applyCalls int
}

func (s *recordingExecutionInboxStore) StageExecution(
	ctx context.Context,
	accountID domain.ExchangeAccountID,
	execution domain.Execution,
	receivedAt time.Time,
	tradingDay string,
) (storage.ExecutionInboxEntry, bool, error) {
	s.mu.Lock()
	s.stageCalls++
	s.mu.Unlock()
	return s.Store.StageExecution(ctx, accountID, execution, receivedAt, tradingDay)
}

func (s *recordingExecutionInboxStore) ApplyStagedExecution(ctx context.Context, id string) (bool, error) {
	s.mu.Lock()
	s.applyCalls++
	s.mu.Unlock()
	return s.Store.ApplyStagedExecution(ctx, id)
}

func (s *recordingExecutionInboxStore) calls() (int, int) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.stageCalls, s.applyCalls
}

func TestLiveExecutionStreamIngestsThroughStageThenApply(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	store, worker, intent, request, fill := seedStagedExecutionBeforeLocalOrder(t)
	adapter := fake.New("fake", exchange.Capabilities{StreamingCandles: true, Sandbox: true})
	remote, err := adapter.PlaceOrder(ctx, request)
	if err != nil {
		t.Fatal(err)
	}
	fill.OrderID = remote.ID
	if _, err := (Runtime{Exchange: adapter, Intents: store}).resolvePendingIntents(ctx); err != nil {
		t.Fatalf("recover local order: %v", err)
	}
	tracked := &recordingExecutionInboxStore{Store: store}
	ready := make(chan struct{}, 1)
	runCtx, cancel := context.WithCancel(ctx)
	done := make(chan error, 1)
	go func() {
		done <- (Runtime{
			Exchange: adapter, Worker: worker,
			Risk: &recordingRisk{decision: RiskDecision{Allowed: true}}, Intents: tracked,
			Subscription: exchange.Subscription{
				InstrumentID: "TEST", Kind: exchange.SubscriptionCandles, Interval: time.Minute,
			},
			Ready: ready,
		}).Run(runCtx)
	}()
	<-ready
	// The fake publishes fills synchronously to the already-open execution stream.
	adapter.Enqueue(fake.Scenario{Kind: fake.OrderMultipleFills, Fills: []domain.Execution{fill}})
	if _, err := adapter.PlaceOrder(ctx, exchange.NewOrder{
		ClientOrderID: "stream-fill-order", StrategyID: intent.StrategyID,
		ExchangeAccountID: "fake", InstrumentID: intent.InstrumentID,
		Side: intent.Side, Type: domain.OrderTypeMarket, Quantity: intent.Quantity,
	}); err != nil {
		t.Fatal(err)
	}
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		stage, apply := tracked.calls()
		if stage == 1 && apply == 1 {
			break
		}
		time.Sleep(time.Millisecond)
	}
	stage, apply := tracked.calls()
	if stage != 1 || apply != 1 {
		t.Fatalf("inbox calls = stage %d, apply %d; want 1, 1", stage, apply)
	}
	cancel()
	if err := <-done; !errors.Is(err, context.Canceled) {
		t.Fatalf("Run() error = %v", err)
	}
}

func seedStagedExecutionBeforeLocalOrder(
	t *testing.T,
) (*sqlite.Store, *strategy.Worker, storage.OrderIntent, exchange.NewOrder, domain.Execution) {
	t.Helper()
	store, worker, signal := seedPendingSignalValues(t, []int64{12, 11, 10, 14})
	intent, audit, request, err := buildIntent(signal)
	if err != nil {
		t.Fatal(err)
	}
	intent.Status = "submitting"
	decision, _, err := buildRiskDecision(signal, RiskDecision{Allowed: true})
	if err != nil {
		t.Fatal(err)
	}
	if err := store.RecordAllowedDecisionIntent(context.Background(), decision, intent, audit); err != nil {
		t.Fatal(err)
	}
	fill := domain.Execution{
		ID: "inbox-fill", StrategyID: signal.StrategyID, InstrumentID: signal.InstrumentID,
		Side: intent.Side, Quantity: domain.Quantity{Value: decimal.NewFromInt(1)},
		Price:      domain.Price{Value: decimal.NewFromInt(100), Asset: "USD"},
		Commission: domain.Money{Amount: decimal.NewFromInt(2), Asset: "USD"},
		ExecutedAt: time.Date(2026, 1, 1, 10, 0, 0, 0, time.UTC), ExchangeTrade: "inbox-trade",
	}
	return store, worker, intent, request, fill
}
