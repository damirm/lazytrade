package agent

import (
	"context"
	"errors"
	"testing"

	"github.com/damirm/lazytrade/internal/exchange"
	"github.com/damirm/lazytrade/internal/exchange/fake"
	"github.com/damirm/lazytrade/internal/storage"
)

func TestSubmitReadyIntentTransitionsThroughSubmittingToSubmitted(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	store, intent, _ := seedFailedStrategyIntents(t, "ready")
	adapter := &placingExchange{Exchange: fake.New("fake", exchange.Capabilities{Sandbox: true})}

	if err := (Runtime{Exchange: adapter, Intents: store}).submitIntent(ctx, intent, requestForIntent(intent)); err != nil {
		t.Fatalf("submit ready intent: %v", err)
	}
	if len(adapter.placements) != 1 {
		t.Fatalf("PlaceOrder calls = %d, want 1", len(adapter.placements))
	}
	assertIntentStatus(t, store, intent.ClientOrderID, "submitted")
	assertIntentAuditSequence(t, store, intent.ID, []string{
		"order_intent_submitting",
		"order_intent_submitted",
	})
}

func TestRestartMaySubmitReadyIntent(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	store, intent, _ := seedFailedStrategyIntents(t, "ready")
	adapter := &placingExchange{Exchange: fake.New("fake", exchange.Capabilities{Sandbox: true})}

	// A durable ready intent represents a process crash before the exchange API
	// boundary. It is the only unresolved phase that restart may submit.
	if err := (Runtime{Exchange: adapter, Intents: store}).submitIntent(ctx, intent, requestForIntent(intent)); err != nil {
		t.Fatalf("restart submit ready intent: %v", err)
	}
	if len(adapter.placements) != 1 {
		t.Fatalf("PlaceOrder calls = %d, want 1", len(adapter.placements))
	}
	assertIntentStatus(t, store, intent.ClientOrderID, "submitted")
}

func TestResolvePendingIntentsBlocksSubmittingAndUnknownNotFoundWithoutPlacement(t *testing.T) {
	t.Parallel()
	for _, status := range []string{"submitting", "unknown"} {
		status := status
		t.Run(status, func(t *testing.T) {
			t.Parallel()
			ctx := context.Background()
			store, intent, _ := seedFailedStrategyIntents(t, status)
			adapter := &placingExchange{Exchange: fake.New("fake", exchange.Capabilities{Sandbox: true})}

			_, err := (Runtime{Exchange: adapter, Intents: store}).resolvePendingIntents(ctx)
			if err == nil {
				t.Fatal("resolve pending intents error = nil, want blocked runtime error")
			}
			var blocked blockedRuntimeError
			if !errors.As(err, &blocked) {
				t.Fatalf("error type = %T (%v), want blockedRuntimeError", err, err)
			}
			if len(adapter.placements) != 0 {
				t.Fatalf("PlaceOrder calls = %d, want 0", len(adapter.placements))
			}
			assertIntentStatus(t, store, intent.ClientOrderID, status)
		})
	}
}

func TestResolvePendingIntentsRecoversFoundSubmittingAsSubmitted(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	store, intent, _ := seedFailedStrategyIntents(t, "submitting")
	base := fake.New("fake", exchange.Capabilities{Sandbox: true})
	if _, err := base.PlaceOrder(ctx, requestForIntent(intent)); err != nil {
		t.Fatalf("seed exchange order: %v", err)
	}
	adapter := &placingExchange{Exchange: base}

	if _, err := (Runtime{Exchange: adapter, Intents: store}).resolvePendingIntents(ctx); err != nil {
		t.Fatalf("resolve submitting intent: %v", err)
	}
	if len(adapter.placements) != 0 {
		t.Fatalf("runtime PlaceOrder calls = %d, want 0", len(adapter.placements))
	}
	assertIntentStatus(t, store, intent.ClientOrderID, "submitted")
}

type failSubmittedResolutionStore struct {
	Store
	failed bool
}

func (s *failSubmittedResolutionStore) ResolveOrderIntent(
	ctx context.Context,
	resolution storage.IntentResolution,
) error {
	if resolution.Status == "submitted" && !s.failed {
		s.failed = true
		return errors.New("simulated crash before submitted commit")
	}
	return s.Store.ResolveOrderIntent(ctx, resolution)
}

func TestCrashAfterExchangeAcceptedOrderRecoversSubmittingByLookup(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	store, intent, _ := seedFailedStrategyIntents(t, "ready")
	base := fake.New("fake", exchange.Capabilities{Sandbox: true})
	failingStore := &failSubmittedResolutionStore{Store: store}

	err := (Runtime{Exchange: base, Intents: failingStore}).submitIntent(ctx, intent, requestForIntent(intent))
	if err == nil {
		t.Fatal("submit ready intent error = nil, want simulated persistence failure")
	}
	assertIntentStatus(t, store, intent.ClientOrderID, "submitting")

	adapter := &placingExchange{Exchange: base}
	if _, err := (Runtime{Exchange: adapter, Intents: store}).resolvePendingIntents(ctx); err != nil {
		t.Fatalf("restart resolve submitting intent: %v", err)
	}
	if len(adapter.placements) != 0 {
		t.Fatalf("restart PlaceOrder calls = %d, want 0", len(adapter.placements))
	}
	assertIntentStatus(t, store, intent.ClientOrderID, "submitted")
}

func assertIntentAuditSequence(t *testing.T, store storage.AuditStore, intentID string, want []string) {
	t.Helper()
	events, err := store.ListAudit(context.Background(), 100)
	if err != nil {
		t.Fatal(err)
	}
	got := make([]string, 0, len(want))
	for _, event := range events {
		if event.ScopeID == "ma-a" && len(got) < len(want) {
			got = append(got, event.EventType)
		}
	}
	if len(got) != len(want) {
		t.Fatalf("intent %s audit = %v, want %v", intentID, got, want)
	}
	for index := range want {
		if got[index] != want[index] {
			t.Fatalf("intent %s audit = %v, want %v", intentID, got, want)
		}
	}
}
