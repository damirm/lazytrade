package agent

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/damirm/lazytrade/internal/domain"
	"github.com/damirm/lazytrade/internal/exchange"
	"github.com/damirm/lazytrade/internal/exchange/fake"
	"github.com/damirm/lazytrade/internal/storage"
	"github.com/damirm/lazytrade/internal/storage/sqlite"
	"github.com/shopspring/decimal"
)

// placingExchange records only placements made through the runtime. Tests may
// seed the embedded exchange before wrapping it without affecting the count.
type placingExchange struct {
	exchange.Exchange
	placements []exchange.NewOrder
}

func (e *placingExchange) PlaceOrder(ctx context.Context, order exchange.NewOrder) (domain.Order, error) {
	e.placements = append(e.placements, order)
	return e.Exchange.PlaceOrder(ctx, order)
}

func TestResolveFailedStrategyIntentsTerminalizesReadyWithoutPlacement(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	store, failed, peer := seedFailedStrategyIntents(t, "ready")
	adapter := &placingExchange{Exchange: fake.New("fake", exchange.Capabilities{Sandbox: true})}
	runtime := Runtime{Exchange: adapter, Intents: store}

	if err := runtime.resolveFailedStrategyIntents(ctx, failed.StrategyID); err != nil {
		t.Fatalf("resolve failed strategy intents: %v", err)
	}
	if len(adapter.placements) != 0 {
		t.Fatalf("PlaceOrder calls = %d, want 0", len(adapter.placements))
	}
	assertIntentStatus(t, store, failed.ClientOrderID, "not_submitted")
	assertIntentStatus(t, store, peer.ClientOrderID, peer.Status)
}

func TestResolveFailedStrategyIntentsRecoversOrderConfirmedByClientID(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	store, failed, peer := seedFailedStrategyIntents(t, "submitting")
	base := fake.New("fake", exchange.Capabilities{Sandbox: true})
	remote, err := base.PlaceOrder(ctx, requestForIntent(failed))
	if err != nil {
		t.Fatalf("seed confirmed order: %v", err)
	}
	adapter := &placingExchange{Exchange: base}
	runtime := Runtime{Exchange: adapter, Intents: store}

	if err := runtime.resolveFailedStrategyIntents(ctx, failed.StrategyID); err != nil {
		t.Fatalf("resolve failed strategy intents: %v", err)
	}
	if len(adapter.placements) != 0 {
		t.Fatalf("runtime PlaceOrder calls = %d, want 0", len(adapter.placements))
	}
	assertIntentStatus(t, store, failed.ClientOrderID, "submitted")
	assertIntentStatus(t, store, peer.ClientOrderID, peer.Status)
	orders, err := base.OpenOrders(ctx, "fake")
	if err != nil {
		t.Fatal(err)
	}
	if len(orders) != 1 || orders[0].ID != remote.ID {
		t.Fatalf("open orders = %#v, want only %s", orders, remote.ID)
	}
}

func TestResolveFailedStrategyIntentsBlocksUnknownIntentProvenMissing(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	store, failed, peer := seedFailedStrategyIntents(t, "unknown")
	adapter := &placingExchange{Exchange: fake.New("fake", exchange.Capabilities{Sandbox: true})}
	runtime := Runtime{Exchange: adapter, Intents: store}

	err := runtime.resolveFailedStrategyIntents(ctx, failed.StrategyID)
	if err == nil {
		t.Fatal("resolve failed strategy intents error = nil, want blocked runtime error")
	}
	var blocked blockedRuntimeError
	if !errors.As(err, &blocked) {
		t.Fatalf("error type = %T (%v), want blockedRuntimeError", err, err)
	}
	if len(adapter.placements) != 0 {
		t.Fatalf("PlaceOrder calls = %d, want 0", len(adapter.placements))
	}
	assertIntentStatus(t, store, failed.ClientOrderID, "unknown")
	assertIntentStatus(t, store, peer.ClientOrderID, peer.Status)
}

func TestResolveFailedStrategyIntentsBlocksSubmittingIntentProvenMissing(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	store, failed, peer := seedFailedStrategyIntents(t, "submitting")
	adapter := &placingExchange{Exchange: fake.New("fake", exchange.Capabilities{Sandbox: true})}

	err := (Runtime{Exchange: adapter, Intents: store}).resolveFailedStrategyIntents(ctx, failed.StrategyID)
	if err == nil {
		t.Fatal("resolve failed strategy intents error = nil, want blocked runtime error")
	}
	var blocked blockedRuntimeError
	if !errors.As(err, &blocked) {
		t.Fatalf("error type = %T (%v), want blockedRuntimeError", err, err)
	}
	if len(adapter.placements) != 0 {
		t.Fatalf("PlaceOrder calls = %d, want 0", len(adapter.placements))
	}
	assertIntentStatus(t, store, failed.ClientOrderID, "submitting")
	assertIntentStatus(t, store, peer.ClientOrderID, peer.Status)
}

func seedFailedStrategyIntents(t *testing.T, failedStatus string) (
	*sqlite.Store, storage.OrderIntent, storage.OrderIntent,
) {
	t.Helper()
	ctx := context.Background()
	store, _, _, _, signals := seedTwoPendingSignals(t)
	now := time.Date(2026, 1, 2, 12, 0, 0, 0, time.UTC)
	quantity := domain.Quantity{Value: decimal.NewFromInt(1)}
	build := func(strategyID domain.StrategyID, status string) storage.OrderIntent {
		signal := signals[strategyID]
		return storage.OrderIntent{
			ID: "intent-" + string(strategyID), SignalID: signal.ID,
			StrategyID: strategyID, ExchangeAccountID: signal.ExchangeAccountID,
			InstrumentID:  signal.InstrumentID,
			ClientOrderID: domain.ClientOrderID("client-" + string(strategyID)),
			Side:          domain.OrderSideBuy, OrderType: domain.OrderTypeMarket,
			Quantity: quantity, Status: status, PayloadChecksum: "checksum-" + string(strategyID),
			CreatedAt: now, UpdatedAt: now,
		}
	}
	failed := build("ma-a", failedStatus)
	peer := build("ma-b", "ready")
	for _, intent := range []storage.OrderIntent{failed, peer} {
		if _, err := store.CreateOrderIntent(ctx, intent); err != nil {
			t.Fatalf("create %s: %v", intent.ID, err)
		}
	}
	return store, failed, peer
}

func assertIntentStatus(t *testing.T, store interface {
	GetOrderIntentByClientOrderID(context.Context, domain.ClientOrderID) (storage.OrderIntent, error)
}, clientID domain.ClientOrderID, want string) {
	t.Helper()
	intent, err := store.GetOrderIntentByClientOrderID(context.Background(), clientID)
	if err != nil {
		t.Fatal(err)
	}
	if intent.Status != want {
		t.Fatalf("intent %s status = %q, want %q", clientID, intent.Status, want)
	}
}
