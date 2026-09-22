package sqlite_test

import (
	"context"
	"encoding/json"
	"errors"
	"testing"

	"github.com/damirm/lazytrade/internal/domain"
	"github.com/damirm/lazytrade/internal/storage"
)

func TestFindExecutionOwnerChecksBothIdentifiersWithinAccount(t *testing.T) {
	store := openStore(t)
	ctx := context.Background()
	signal := registerAndCommitSignal(t, store)
	for index, suffix := range []string{"1", "2"} {
		if index > 0 {
			signal.ID = "signal-2"
			signal.CausativeCursor.Sequence++
			if err := store.CommitEvent(ctx, storage.StrategyEventCommit{
				StrategyID: signal.StrategyID, ExpectedVersion: 1, StateVersion: 1,
				StatePayload: json.RawMessage(`{}`), StateChecksum: "second-state",
				EventCursor: signal.CausativeCursor, Signals: []domain.Signal{signal}, UpdatedAt: fixedTime,
			}); err != nil {
				t.Fatal(err)
			}
		}
		intent := storage.OrderIntent{
			ID: "intent-" + suffix, SignalID: signal.ID, StrategyID: signal.StrategyID,
			ExchangeAccountID: signal.ExchangeAccountID, InstrumentID: signal.InstrumentID,
			ClientOrderID: domain.ClientOrderID("client-" + suffix), Side: domain.OrderSideBuy,
			OrderType: domain.OrderTypeMarket, Quantity: signal.Quantity, Status: "submitting",
			PayloadChecksum: "checksum-" + suffix, CreatedAt: fixedTime, UpdatedAt: fixedTime,
		}
		recordAllowedIntent(t, store, intent)
		// Leave the second intent in-flight, with no local exchange order yet.
		if index > 0 {
			continue
		}
		zero, _ := domain.NewQuantity("0")
		if err := store.ResolveOrderIntent(ctx, storage.IntentResolution{
			IntentID: intent.ID, Status: "submitted",
			Order: &storage.ExchangeOrder{
				ID: "local-order-1", OrderIntentID: intent.ID, ExchangeAccountID: intent.ExchangeAccountID,
				ExchangeOrderID: "order-1", Status: "filled", RequestedQuantity: intent.Quantity,
				FilledQuantity: zero, SubmittedAt: fixedTime, UpdatedAt: fixedTime,
			},
			Audit: storage.AuditEvent{
				ID: "submitted-1", EventType: "order_intent_submitted", Actor: "test",
				ScopeType: "order_intent", ScopeID: intent.ID, Payload: json.RawMessage(`{}`), CreatedAt: fixedTime,
			},
		}); err != nil {
			t.Fatal(err)
		}
	}
	for _, test := range []struct {
		name    string
		account domain.ExchangeAccountID
		order   domain.OrderID
		client  domain.ClientOrderID
		want    error
	}{
		{"both IDs of terminal order", "account-1", "order-1", "client-1", nil},
		{"order ID only", "account-1", "order-1", "", nil},
		{"client ID before response persisted", "account-1", "order-2", "client-2", nil},
		{"foreign account", "account-2", "order-1", "client-1", storage.ErrNotFound},
		{"unknown order", "account-1", "unknown", "unknown", storage.ErrNotFound},
		{"unknown order without client", "account-1", "unknown", "", storage.ErrNotFound},
		{"order disagrees with client", "account-1", "unknown", "client-1", storage.ErrConflict},
		{"client disagrees with order", "account-1", "order-1", "unknown", storage.ErrConflict},
		{"IDs identify different intents", "account-1", "order-1", "client-2", storage.ErrConflict},
	} {
		t.Run(test.name, func(t *testing.T) {
			owner, err := store.FindExecutionOwner(ctx, test.account, test.order, test.client)
			if !errors.Is(err, test.want) {
				t.Fatalf("owner = %+v, error = %v, want %v", owner, err, test.want)
			}
			if err == nil && (owner.StrategyID != signal.StrategyID || owner.InstrumentID != signal.InstrumentID || owner.Side != domain.OrderSideBuy) {
				t.Fatalf("owner = %+v", owner)
			}
		})
	}
	// Simulate inconsistent persisted state created outside the validated writer.
	if _, err := store.DB().ExecContext(ctx, "UPDATE orders SET exchange_account_id = ? WHERE id = ?", "account-2", "local-order-1"); err != nil {
		t.Fatal(err)
	}
	for _, clientID := range []domain.ClientOrderID{"", "client-1"} {
		if _, err := store.FindExecutionOwner(ctx, "account-1", "order-1", clientID); !errors.Is(err, storage.ErrConflict) {
			t.Fatalf("mismatched persisted account: %v, want conflict", err)
		}
	}
}

func TestResolveOrderIntentRejectsDifferentAccountAtomically(t *testing.T) {
	store := openStore(t)
	ctx := context.Background()
	signal := registerAndCommitSignal(t, store)
	intent := storage.OrderIntent{
		ID: "intent", SignalID: signal.ID, StrategyID: signal.StrategyID,
		ExchangeAccountID: signal.ExchangeAccountID, InstrumentID: signal.InstrumentID,
		ClientOrderID: "client", Side: domain.OrderSideBuy, OrderType: domain.OrderTypeMarket,
		Quantity: signal.Quantity, Status: "submitting", PayloadChecksum: "checksum",
		CreatedAt: fixedTime, UpdatedAt: fixedTime,
	}
	recordAllowedIntent(t, store, intent)
	zero, _ := domain.NewQuantity("0")
	err := store.ResolveOrderIntent(ctx, storage.IntentResolution{
		IntentID: intent.ID, Status: "submitted",
		Order: &storage.ExchangeOrder{
			ID: "local-order", OrderIntentID: intent.ID, ExchangeAccountID: "different-account",
			ExchangeOrderID: "order", Status: "accepted", RequestedQuantity: intent.Quantity,
			FilledQuantity: zero, SubmittedAt: fixedTime, UpdatedAt: fixedTime,
		},
		Audit: storage.AuditEvent{
			ID: "submitted", EventType: "order_intent_submitted", Actor: "test",
			ScopeType: "order_intent", ScopeID: intent.ID, Payload: json.RawMessage(`{}`), CreatedAt: fixedTime,
		},
	})
	if !errors.Is(err, storage.ErrConflict) {
		t.Fatalf("cross-account resolution error = %v, want conflict", err)
	}
	persisted, err := store.GetOrderIntentByClientOrderID(ctx, intent.ClientOrderID)
	if err != nil || persisted.Status != "submitting" {
		t.Fatalf("intent after rollback = %+v, error = %v", persisted, err)
	}
	var orders int
	if err := store.DB().QueryRowContext(ctx, "SELECT count(*) FROM orders").Scan(&orders); err != nil || orders != 0 {
		t.Fatalf("orders after rollback = %d, error = %v", orders, err)
	}
	audits, err := store.ListAudit(ctx, 10)
	if err != nil || len(audits) != 1 {
		t.Fatalf("audit after rollback = %+v, error = %v", audits, err)
	}
}
