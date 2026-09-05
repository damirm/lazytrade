package agent

import (
	"context"
	"encoding/json"
	"testing"

	"github.com/damirm/lazytrade/internal/domain"
	"github.com/damirm/lazytrade/internal/storage"
)

func TestTerminalizeFailedStrategySignalsRejectsOnlyOwningStrategy(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	store, _, _, _, pending := seedTwoPendingSignals(t)
	runtime := Runtime{Intents: store}

	if err := runtime.terminalizeFailedStrategySignals(ctx, "ma-a"); err != nil {
		t.Fatalf("terminalize failed strategy signals: %v", err)
	}

	failedPending, err := store.ListSignalsPendingRiskByStrategy(ctx, "ma-a", 10)
	if err != nil {
		t.Fatal(err)
	}
	if len(failedPending) != 0 {
		t.Fatalf("failed strategy pending signals = %#v, want none", failedPending)
	}

	peerPending, err := store.ListSignalsPendingRiskByStrategy(ctx, "ma-b", 10)
	if err != nil {
		t.Fatal(err)
	}
	if len(peerPending) != 1 || peerPending[0].ID != pending["ma-b"].ID {
		t.Fatalf("peer pending signals = %#v, want only %s", peerPending, pending["ma-b"].ID)
	}

	assertStrategyFailedRiskDecision(t, store, pending["ma-a"])

	risks := map[domain.StrategyID]SignalRisk{
		"ma-b": &recordingRisk{decision: RiskDecision{
			Allowed: false, ReasonCode: "peer_test", Reason: "processed by peer risk gate",
		}},
	}
	if err := runtime.processSignalWith(ctx, peerPending[0], risks); err != nil {
		t.Fatalf("process peer pending signal: %v", err)
	}
	peerPending, err = store.ListSignalsPendingRiskByStrategy(ctx, "ma-b", 10)
	if err != nil {
		t.Fatal(err)
	}
	if len(peerPending) != 0 {
		t.Fatalf("peer pending signals after processing = %#v, want none", peerPending)
	}
}

func TestTerminalizeFailedStrategySignalsIsIdempotent(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	store, _, _, _, pending := seedTwoPendingSignals(t)
	runtime := Runtime{Intents: store}

	if err := runtime.terminalizeFailedStrategySignals(ctx, "ma-a"); err != nil {
		t.Fatalf("first terminalization: %v", err)
	}
	if err := runtime.terminalizeFailedStrategySignals(ctx, "ma-a"); err != nil {
		t.Fatalf("repeated terminalization: %v", err)
	}

	assertStrategyFailedRiskDecision(t, store, pending["ma-a"])
	audit, err := store.ListAudit(ctx, 10)
	if err != nil {
		t.Fatal(err)
	}
	if len(audit) != 1 {
		t.Fatalf("audit events after repeated terminalization = %d, want 1", len(audit))
	}
}

func assertStrategyFailedRiskDecision(t *testing.T, store interface {
	ListAudit(context.Context, uint32) ([]storage.AuditEvent, error)
}, signal domain.Signal) {
	t.Helper()
	audit, err := store.ListAudit(context.Background(), 10)
	if err != nil {
		t.Fatal(err)
	}
	for _, event := range audit {
		if event.EventType != "risk_decision" || event.ScopeID != string(signal.StrategyID) {
			continue
		}
		var payload struct {
			SignalID   domain.SignalID `json:"signal_id"`
			Decision   string          `json:"decision"`
			ReasonCode string          `json:"reason_code"`
		}
		if err := json.Unmarshal(event.Payload, &payload); err != nil {
			t.Fatal(err)
		}
		if payload.SignalID != signal.ID || payload.Decision != "reject" ||
			payload.ReasonCode != "strategy_failed" {
			t.Fatalf("failed strategy decision = %#v", payload)
		}
		return
	}
	t.Fatalf("strategy_failed risk decision for signal %s not found", signal.ID)
}
