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
	"github.com/shopspring/decimal"
)

func TestExecutionPumpUsesDurableOwnershipBeforeResponseAndAfterRestart(t *testing.T) {
	for _, restart := range []bool{false, true} {
		name := "before_place_order_response"
		if restart {
			name = "immediately_after_restart_without_client_id"
		}
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			ctx := context.Background()
			path := filepath.Join(t.TempDir(), "executions.db")
			store, _, signal := seedPendingSignalValuesAtPath(t, []int64{12, 11, 10, 14}, path)
			intent, audit, request, err := buildIntent(signal)
			if err != nil {
				t.Fatal(err)
			}
			intent.Status = "submitting"
			decision, _, err := buildRiskDecision(signal, RiskDecision{Allowed: true})
			if err != nil {
				t.Fatal(err)
			}
			if err := store.RecordAllowedDecisionIntent(ctx, decision, intent, audit); err != nil {
				t.Fatal(err)
			}
			adapter := fake.New("fake", exchange.Capabilities{Sandbox: true})
			remote, err := adapter.PlaceOrder(ctx, request)
			if err != nil {
				t.Fatal(err)
			}
			runtime := Runtime{Intents: store, Strategies: []StrategyBinding{{
				ID: intent.StrategyID,
				TradingDayKey: func(at time.Time) string {
					return at.In(time.FixedZone("UTC+3", 3*60*60)).Format("2006-01-02")
				},
			}}}
			fill := exchange.Execution{
				ID: "early-fill", OrderID: remote.ID, ClientOrderID: intent.ClientOrderID,
				InstrumentID: intent.InstrumentID, Side: intent.Side, Quantity: intent.Quantity,
				Price:      domain.Price{Value: decimal.NewFromInt(100), Asset: "USD"},
				Commission: domain.Money{Amount: decimal.NewFromInt(2), Asset: "USD"},
				ExecutedAt: time.Date(2026, 1, 1, 22, 0, 0, 0, time.UTC), ExchangeTrade: "early-trade",
			}
			if restart {
				if err := runtime.recordSubmitted(ctx, intent, remote, "test"); err != nil {
					t.Fatal(err)
				}
				if err := store.Close(); err != nil {
					t.Fatal(err)
				}
				store, err = sqlite.Open(ctx, path)
				if err != nil {
					t.Fatal(err)
				}
				t.Cleanup(func() { _ = store.Close() })
				runtime.Intents = store
				fill.ClientOrderID = "" // Exchange order identity alone survives restart.
			}
			// Start ingress without reconciliation or any adapter context registration.
			executions, notifications, pumpErrors := startAttributionTestPump(t, runtime, "fake")
			executions <- fill
			awaitStagedExecution(t, notifications, pumpErrors)
			pending, err := store.ListPendingExecutions(ctx, "fake", 10)
			if err != nil || len(pending) != 1 {
				t.Fatalf("pending = %+v, error = %v", pending, err)
			}
			entry := pending[0]
			if entry.Execution.StrategyID != intent.StrategyID || entry.TradingDay != "2026-01-02" {
				t.Fatalf("attributed entry = %+v", entry)
			}
			if !restart {
				if _, err := store.ApplyStagedExecution(ctx, entry.ID); !errors.Is(err, storage.ErrNotFound) {
					t.Fatalf("apply before local order: %v", err)
				}
				if err := runtime.recordSubmitted(ctx, intent, remote, "test"); err != nil {
					t.Fatal(err)
				}
			}
			if applied, err := store.ApplyStagedExecution(ctx, entry.ID); err != nil || !applied {
				t.Fatalf("apply = %t, error = %v", applied, err)
			}
			executions <- fill
			awaitStagedExecution(t, notifications, pumpErrors)
			if applied, err := store.ApplyStagedExecution(ctx, entry.ID); err != nil || applied {
				t.Fatalf("duplicate apply = %t, error = %v", applied, err)
			}
			position, err := store.LoadPosition(ctx, intent.StrategyID, intent.InstrumentID)
			if err != nil || position.Revision != 1 || !position.Quantity.Value.Equal(fill.Quantity.Value) {
				t.Fatalf("position = %+v, error = %v", position, err)
			}
			statistics, err := store.LoadDailyStatistics(ctx, intent.StrategyID, "2026-01-02", "USD")
			if err != nil || !statistics.Commissions.Equal(fill.Commission.Amount) {
				t.Fatalf("statistics = %+v, error = %v", statistics, err)
			}
		})
	}
}

func TestExecutionPumpRejectsUnknownOrConflictingOwnership(t *testing.T) {
	for _, test := range []struct {
		name    string
		change  func(*exchange.Execution)
		account domain.ExchangeAccountID
		want    error
	}{
		{"unknown order", func(e *exchange.Execution) { e.ClientOrderID = "unknown" }, "fake", storage.ErrNotFound},
		{"foreign account", func(*exchange.Execution) {}, "other", storage.ErrNotFound},
		{"wrong instrument", func(e *exchange.Execution) { e.InstrumentID = "OTHER" }, "fake", storage.ErrConflict},
		{"wrong side", func(e *exchange.Execution) { e.Side = domain.OrderSideSell }, "fake", storage.ErrConflict},
	} {
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()
			store, _, intent, _, fill := seedStagedExecutionBeforeLocalOrder(t)
			incoming := exchange.Execution{
				ID: fill.ID, OrderID: "early-order", ClientOrderID: intent.ClientOrderID,
				InstrumentID: fill.InstrumentID, Side: fill.Side, Quantity: fill.Quantity,
				Price: fill.Price, Commission: fill.Commission, ExecutedAt: fill.ExecutedAt,
			}
			test.change(&incoming)
			executions, _, pumpErrors := startAttributionTestPump(t, Runtime{Intents: store}, test.account)
			executions <- incoming
			select {
			case err := <-pumpErrors:
				if !errors.Is(err, test.want) {
					t.Fatalf("pump error = %v, want %v", err, test.want)
				}
			case <-time.After(2 * time.Second):
				t.Fatal("pump did not fail closed")
			}
			pending, err := store.ListPendingExecutions(context.Background(), test.account, 10)
			if err != nil || len(pending) != 0 {
				t.Fatalf("unattributed fills in inbox = %+v, error = %v", pending, err)
			}
		})
	}
}

func startAttributionTestPump(t *testing.T, runtime Runtime, account domain.ExchangeAccountID) (chan<- exchange.Execution, <-chan struct{}, <-chan error) {
	t.Helper()
	ctx, cancel := context.WithCancel(context.Background())
	executions := make(chan exchange.Execution, 1)
	notifications, pumpErrors := runtime.startExecutionPump(ctx, account, exchange.ExecutionStream{
		Executions: executions, Errors: make(chan error),
	}, &sync.Mutex{})
	t.Cleanup(func() {
		cancel()
		for range pumpErrors {
		}
	})
	return executions, notifications, pumpErrors
}

func awaitStagedExecution(t *testing.T, notifications <-chan struct{}, pumpErrors <-chan error) {
	t.Helper()
	select {
	case _, ok := <-notifications:
		if !ok {
			t.Fatal("pump closed before staging execution")
		}
	case err := <-pumpErrors:
		t.Fatalf("pump failed before staging execution: %v", err)
	case <-time.After(2 * time.Second):
		t.Fatal("pump did not stage execution")
	}
}
