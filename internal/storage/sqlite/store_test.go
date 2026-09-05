package sqlite_test

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"path/filepath"
	"testing"
	"time"

	"github.com/damirm/lazytrade/internal/domain"
	"github.com/damirm/lazytrade/internal/storage"
	storesqlite "github.com/damirm/lazytrade/internal/storage/sqlite"
)

var fixedTime = time.Date(2026, 7, 29, 12, 0, 0, 123000000, time.UTC)

func openStore(t *testing.T) *storesqlite.Store {
	t.Helper()
	store, err := storesqlite.Open(context.Background(), filepath.Join(t.TempDir(), "lazytrade.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if err := store.Close(); err != nil {
			t.Errorf("close store: %v", err)
		}
	})
	return store
}

func registerAndCommitSignal(t *testing.T, store *storesqlite.Store) domain.Signal {
	t.Helper()
	ctx := context.Background()
	definition := storage.StrategyDefinition{
		ID: "strategy-1", ExchangeAccountID: "account-1", InstrumentID: "instrument-1",
		StrategyType: "moving_average_cross", ConfigHash: "abc123",
		CreatedAt: fixedTime, UpdatedAt: fixedTime,
	}
	if err := store.RegisterStrategy(ctx, definition); err != nil {
		t.Fatal(err)
	}
	quantity, _ := domain.NewQuantity("1.25")
	cursor := domain.EventCursor{Timestamp: fixedTime, Priority: 4, Sequence: 9}
	signal := domain.Signal{
		ID: "signal-1", StrategyID: definition.ID, ExchangeAccountID: definition.ExchangeAccountID,
		InstrumentID: definition.InstrumentID, Action: domain.SignalBuy,
		OrderType: domain.OrderTypeMarket, Quantity: quantity, CreatedAt: fixedTime,
		CausativeCursor: cursor, Ordinal: 0, ReasonCode: "cross",
	}
	err := store.CommitEvent(ctx, storage.StrategyEventCommit{
		StrategyID: definition.ID, ExpectedVersion: 0, StateVersion: 1,
		StatePayload:  json.RawMessage(`{"fast":"10","slow":"20"}`),
		RuntimeStatus: "running", EventCursor: cursor, StateChecksum: "state-checksum",
		Signals: []domain.Signal{signal}, UpdatedAt: fixedTime,
	})
	if err != nil {
		t.Fatal(err)
	}
	return signal
}

func recordAllowedIntent(t *testing.T, store *storesqlite.Store, intent storage.OrderIntent) {
	t.Helper()
	err := store.RecordAllowedDecisionIntent(context.Background(), storage.RiskDecision{
		ID: "decision-" + intent.ID, SignalID: intent.SignalID, Decision: "allow",
		ReasonCode: "test_allowed", Payload: json.RawMessage(`{}`), CreatedAt: intent.CreatedAt,
	}, intent, storage.AuditEvent{
		ID: "audit-" + intent.ID, EventType: "risk_allowed", Actor: "test",
		ScopeType: "order_intent", ScopeID: intent.ID,
		Payload: json.RawMessage(`{}`), CreatedAt: intent.CreatedAt,
	})
	if err != nil {
		t.Fatalf("record allowed intent %s: %v", intent.ID, err)
	}
}

func TestMigrationAndPragmas(t *testing.T) {
	store := openStore(t)
	ctx := context.Background()
	if err := store.Migrate(ctx); err != nil {
		t.Fatalf("repeat migration: %v", err)
	}
	var foreignKeys, busyTimeout int
	var journalMode string
	if err := store.DB().QueryRowContext(ctx, "PRAGMA foreign_keys").Scan(&foreignKeys); err != nil {
		t.Fatal(err)
	}
	if err := store.DB().QueryRowContext(ctx, "PRAGMA busy_timeout").Scan(&busyTimeout); err != nil {
		t.Fatal(err)
	}
	if err := store.DB().QueryRowContext(ctx, "PRAGMA journal_mode").Scan(&journalMode); err != nil {
		t.Fatal(err)
	}
	if foreignKeys != 1 || busyTimeout != 5000 || journalMode != "wal" {
		t.Fatalf("pragmas foreign_keys=%d busy_timeout=%d journal=%s", foreignKeys, busyTimeout, journalMode)
	}
	var version, dirty int
	if err := store.DB().QueryRowContext(ctx,
		"SELECT version, dirty FROM schema_migrations ORDER BY version DESC LIMIT 1").
		Scan(&version, &dirty); err != nil {
		t.Fatal(err)
	}
	if version != 5 || dirty != 0 {
		t.Fatalf("schema version=%d dirty=%d", version, dirty)
	}
}

func TestMigrationRejectsDirtyAndNewerSchema(t *testing.T) {
	t.Run("dirty", func(t *testing.T) {
		store := openStore(t)
		if _, err := store.DB().Exec("UPDATE schema_migrations SET dirty=1 WHERE version=5"); err != nil {
			t.Fatal(err)
		}
		if err := store.Migrate(context.Background()); err == nil {
			t.Fatal("expected dirty schema error")
		}
	})
	t.Run("newer", func(t *testing.T) {
		store := openStore(t)
		if _, err := store.DB().Exec(
			"INSERT INTO schema_migrations(version, dirty, applied_at) VALUES (999,0,0)",
		); err != nil {
			t.Fatal(err)
		}
		if err := store.Migrate(context.Background()); err == nil {
			t.Fatal("expected newer schema error")
		}
	})
}

func TestDurableIntentPhaseTransitionIsAtomicCAS(t *testing.T) {
	store := openStore(t)
	signal := registerAndCommitSignal(t, store)
	quantity, _ := domain.NewQuantity("1")
	intent := storage.OrderIntent{
		ID: "intent-phase", SignalID: signal.ID, StrategyID: signal.StrategyID,
		ExchangeAccountID: signal.ExchangeAccountID, InstrumentID: signal.InstrumentID,
		ClientOrderID: "client-phase", Side: domain.OrderSideBuy,
		OrderType: domain.OrderTypeMarket, Quantity: quantity, Status: "ready",
		PayloadChecksum: "phase-checksum", CreatedAt: fixedTime, UpdatedAt: fixedTime,
	}
	ctx := context.Background()
	recordAllowedIntent(t, store, intent)
	audit := storage.AuditEvent{
		ID: "audit-submitting", EventType: "order_intent_submitting", Actor: "agent",
		ScopeType: "order_intent", ScopeID: intent.ID, Payload: json.RawMessage(`{"from":"ready"}`),
		CreatedAt: fixedTime.Add(time.Second),
	}
	if err := store.TransitionOrderIntent(ctx, storage.IntentTransition{
		IntentID: intent.ID, FromStatus: "ready", ToStatus: "submitting", Audit: audit,
	}); err != nil {
		t.Fatal(err)
	}
	got, err := store.GetOrderIntentByClientOrderID(ctx, intent.ClientOrderID)
	if err != nil || got.Status != "submitting" || !got.UpdatedAt.Equal(audit.CreatedAt) {
		t.Fatalf("transitioned intent=%+v error=%v", got, err)
	}
	staleAudit := audit
	staleAudit.ID = "audit-stale"
	if err := store.TransitionOrderIntent(ctx, storage.IntentTransition{
		IntentID: intent.ID, FromStatus: "ready", ToStatus: "unknown", Audit: staleAudit,
	}); !errors.Is(err, storage.ErrConflict) {
		t.Fatalf("stale transition error=%v", err)
	}
	events, err := store.ListAudit(ctx, 10)
	if err != nil || len(events) != 2 || events[1].ID != audit.ID {
		t.Fatalf("audit after stale CAS=%+v error=%v", events, err)
	}
	unresolved, err := store.ListPendingOrderIntents(ctx, 10)
	if err != nil || len(unresolved) != 1 || unresolved[0].Status != "submitting" {
		t.Fatalf("unresolved intents=%+v error=%v", unresolved, err)
	}
}

func TestMigrationConvertsLegacyPendingIntentToUnknown(t *testing.T) {
	path := filepath.Join(t.TempDir(), "legacy.db")
	db, err := sql.Open("sqlite", path)
	if err != nil {
		t.Fatal(err)
	}
	_, err = db.Exec(`
		CREATE TABLE schema_migrations(version INTEGER PRIMARY KEY, dirty INTEGER NOT NULL, applied_at INTEGER NOT NULL);
		INSERT INTO schema_migrations(version, dirty, applied_at) VALUES (2,0,0);
		CREATE TABLE order_intents(id TEXT PRIMARY KEY, status TEXT NOT NULL);
		INSERT INTO order_intents(id,status) VALUES ('legacy','pending'),('uncertain','unknown');
	`)
	if err != nil {
		db.Close()
		t.Fatal(err)
	}
	if err := db.Close(); err != nil {
		t.Fatal(err)
	}
	store, err := storesqlite.Open(context.Background(), path)
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	rows, err := store.DB().Query("SELECT id,status FROM order_intents ORDER BY id")
	if err != nil {
		t.Fatal(err)
	}
	defer rows.Close()
	for rows.Next() {
		var id, status string
		if err := rows.Scan(&id, &status); err != nil {
			t.Fatal(err)
		}
		if status != "unknown" {
			t.Fatalf("intent %s status=%s, want unknown", id, status)
		}
	}
}

func TestStrategyStateAndSignalsAreAtomicAndVersioned(t *testing.T) {
	store := openStore(t)
	signal := registerAndCommitSignal(t, store)
	ctx := context.Background()
	runtime, err := store.LoadRuntime(ctx, "strategy-1")
	if err != nil {
		t.Fatal(err)
	}
	if runtime.Revision != 1 || runtime.StateChecksum != "state-checksum" {
		t.Fatalf("unexpected runtime: %+v", runtime)
	}

	// Exact replay after an unknown local outcome is idempotent.
	if err := store.CommitEvent(ctx, storage.StrategyEventCommit{
		StrategyID: "strategy-1", ExpectedVersion: 0, StateVersion: 1,
		StatePayload:  json.RawMessage(`{"fast":"10","slow":"20"}`),
		RuntimeStatus: "running", EventCursor: signal.CausativeCursor,
		StateChecksum: "state-checksum", Signals: []domain.Signal{signal}, UpdatedAt: fixedTime,
	}); err != nil {
		t.Fatalf("exact replay: %v", err)
	}
	if err := store.CommitEvent(ctx, storage.StrategyEventCommit{
		StrategyID: "strategy-1", ExpectedVersion: 0, StateVersion: 1,
		StatePayload: json.RawMessage(`{"different":true}`), RuntimeStatus: "running",
		EventCursor: signal.CausativeCursor, StateChecksum: "different", UpdatedAt: fixedTime,
	}); !errors.Is(err, storage.ErrVersionConflict) {
		t.Fatalf("version conflict error=%v", err)
	}
}

func TestStrategyLifecycleUpdatePreservesSnapshotAndRevision(t *testing.T) {
	store := openStore(t)
	signal := registerAndCommitSignal(t, store)
	ctx := context.Background()
	before, err := store.LoadRuntime(ctx, signal.StrategyID)
	if err != nil {
		t.Fatal(err)
	}
	updatedAt := fixedTime.Add(time.Hour)
	if err := store.SetStrategyStatus(ctx, signal.StrategyID, "stopped", "", updatedAt); err != nil {
		t.Fatal(err)
	}
	after, err := store.LoadRuntime(ctx, signal.StrategyID)
	if err != nil {
		t.Fatal(err)
	}
	if after.RuntimeStatus != "stopped" || after.StatusReason != "" ||
		after.Revision != before.Revision || after.StateChecksum != before.StateChecksum ||
		after.EventCursor != before.EventCursor ||
		string(after.StatePayload) != string(before.StatePayload) ||
		!after.UpdatedAt.Equal(updatedAt) {
		t.Fatalf("lifecycle update changed snapshot: before=%+v after=%+v", before, after)
	}
}

func TestStrategyLifecycleUpdateRequiresExistingRuntime(t *testing.T) {
	store := openStore(t)
	err := store.SetStrategyStatus(context.Background(), "missing", "stopped", "", fixedTime)
	if !errors.Is(err, storage.ErrNotFound) {
		t.Fatalf("error = %v", err)
	}
}

func TestAllowedIntentDuplicateConflictsWithoutChangingPersistedIntent(t *testing.T) {
	store := openStore(t)
	signal := registerAndCommitSignal(t, store)
	quantity, _ := domain.NewQuantity("1.25")
	intent := storage.OrderIntent{
		ID: "intent-1", SignalID: signal.ID, StrategyID: signal.StrategyID,
		ExchangeAccountID: signal.ExchangeAccountID, InstrumentID: signal.InstrumentID,
		ClientOrderID: "client-1", Side: domain.OrderSideBuy, OrderType: domain.OrderTypeMarket,
		Quantity: quantity, Status: "created", PayloadChecksum: "intent-checksum",
		CreatedAt: fixedTime, UpdatedAt: fixedTime,
	}
	recordAllowedIntent(t, store, intent)
	err := store.RecordAllowedDecisionIntent(context.Background(), storage.RiskDecision{
		ID: "decision-duplicate", SignalID: signal.ID, Decision: "allow",
		ReasonCode: "test_allowed", Payload: json.RawMessage(`{}`), CreatedAt: fixedTime,
	}, intent, storage.AuditEvent{
		ID: "audit-duplicate", EventType: "risk_allowed", Actor: "test",
		ScopeType: "order_intent", ScopeID: intent.ID,
		Payload: json.RawMessage(`{}`), CreatedAt: fixedTime,
	})
	if !errors.Is(err, storage.ErrConflict) {
		t.Fatalf("duplicate allowed intent error=%v", err)
	}
	got, err := store.GetOrderIntentByClientOrderID(context.Background(), intent.ClientOrderID)
	if err != nil || got.ID != intent.ID || got.PayloadChecksum != intent.PayloadChecksum {
		t.Fatalf("persisted intent=%+v error=%v", got, err)
	}
}

func TestExecutionInboxStagesThenAppliesAtomically(t *testing.T) {
	store := openStore(t)
	signal := registerAndCommitSignal(t, store)
	quantity, _ := domain.NewQuantity("2")
	zero, _ := domain.NewQuantity("0")
	intent := storage.OrderIntent{
		ID: "inbox-intent", SignalID: signal.ID, StrategyID: signal.StrategyID,
		ExchangeAccountID: signal.ExchangeAccountID, InstrumentID: signal.InstrumentID,
		ClientOrderID: "inbox-client", Side: domain.OrderSideBuy, OrderType: domain.OrderTypeMarket,
		Quantity: quantity, Status: "ready", PayloadChecksum: "inbox-intent-checksum",
		CreatedAt: fixedTime, UpdatedAt: fixedTime,
	}
	recordAllowedIntent(t, store, intent)
	order := storage.ExchangeOrder{
		ID: "local-order", OrderIntentID: intent.ID, ExchangeAccountID: intent.ExchangeAccountID,
		ExchangeOrderID: "exchange-order", Status: "accepted", RequestedQuantity: quantity,
		FilledQuantity: zero, SubmittedAt: fixedTime, UpdatedAt: fixedTime,
	}
	if err := store.ResolveOrderIntent(context.Background(), storage.IntentResolution{
		IntentID: intent.ID, Status: "submitted", Order: &order,
		Audit: storage.AuditEvent{ID: "inbox-submit-audit", EventType: "order_intent_submitted", Actor: "test",
			ScopeType: "order_intent", ScopeID: intent.ID, Payload: json.RawMessage(`{}`), CreatedAt: fixedTime},
	}); err != nil {
		t.Fatal(err)
	}
	price, _ := domain.NewPrice("100", "RUB")
	commission, _ := domain.NewMoney("0.2", "RUB")
	execution := domain.Execution{
		ID: "execution-inbox-1", OrderID: order.ExchangeOrderID, StrategyID: signal.StrategyID,
		InstrumentID: signal.InstrumentID, Side: domain.OrderSideBuy, Quantity: quantity,
		Price: price, Commission: commission, ExecutedAt: fixedTime.Add(time.Minute), ExchangeTrade: "trade-inbox-1",
	}
	ctx := context.Background()
	entry, inserted, err := store.StageExecution(ctx, signal.ExchangeAccountID, execution, fixedTime.Add(2*time.Minute), "2026-07-29")
	if err != nil || !inserted || entry.Status != "pending" {
		t.Fatalf("stage entry=%+v inserted=%v error=%v", entry, inserted, err)
	}
	duplicate, inserted, err := store.StageExecution(ctx, signal.ExchangeAccountID, execution, fixedTime.Add(2*time.Minute), "2026-07-29")
	if err != nil || inserted || duplicate.ID != entry.ID {
		t.Fatalf("duplicate entry=%+v inserted=%v error=%v", duplicate, inserted, err)
	}
	commissionVariant := execution
	commissionVariant.Commission, _ = domain.NewMoney("0.5", "RUB")
	if sameFill, inserted, err := store.StageExecution(ctx, signal.ExchangeAccountID, commissionVariant, fixedTime.Add(2*time.Minute), "2026-07-29"); err != nil || inserted || sameFill.ID != entry.ID {
		t.Fatalf("commission variant entry=%+v inserted=%v error=%v", sameFill, inserted, err)
	}
	changed := execution
	changed.Quantity, _ = domain.NewQuantity("1")
	if _, _, err := store.StageExecution(ctx, signal.ExchangeAccountID, changed, fixedTime.Add(2*time.Minute), "2026-07-29"); !errors.Is(err, storage.ErrConflict) {
		t.Fatalf("changed duplicate error=%v", err)
	}
	pending, err := store.ListPendingExecutions(ctx, signal.ExchangeAccountID, 10)
	if err != nil || len(pending) != 1 || pending[0].ID != entry.ID {
		t.Fatalf("pending=%+v error=%v", pending, err)
	}
	applied, err := store.ApplyStagedExecution(ctx, entry.ID)
	if err != nil || !applied {
		t.Fatalf("apply=%v error=%v", applied, err)
	}
	if applied, err = store.ApplyStagedExecution(ctx, entry.ID); err != nil || applied {
		t.Fatalf("repeat apply=%v error=%v", applied, err)
	}
	pending, err = store.ListPendingExecutions(ctx, signal.ExchangeAccountID, 10)
	if err != nil || len(pending) != 0 {
		t.Fatalf("pending after apply=%+v error=%v", pending, err)
	}
	position, err := store.LoadPosition(ctx, signal.StrategyID, signal.InstrumentID)
	if err != nil || !position.Quantity.Value.Equal(quantity.Value) {
		t.Fatalf("position=%+v error=%v", position, err)
	}
	statistics, err := store.LoadDailyStatistics(ctx, signal.StrategyID, "2026-07-29", "RUB")
	if err != nil || statistics.Commissions.String() != "0.2" || statistics.TradeCount != 0 {
		t.Fatalf("initial commission statistics=%+v error=%v", statistics, err)
	}
	cumulative, _ := domain.NewMoney("0.5", "RUB")
	delta, commissionApplied, err := store.ApplyCumulativeOrderCommission(ctx, signal.ExchangeAccountID, order.ExchangeOrderID, cumulative, fixedTime.Add(3*time.Minute), "2026-07-29")
	if err != nil || !commissionApplied || delta.Amount.String() != "0.3" || delta.Asset != "RUB" {
		t.Fatalf("cumulative delta=%+v applied=%v error=%v", delta, commissionApplied, err)
	}
	if delta, commissionApplied, err = store.ApplyCumulativeOrderCommission(ctx, signal.ExchangeAccountID, order.ExchangeOrderID, cumulative, fixedTime.Add(4*time.Minute), "2026-07-29"); err != nil || commissionApplied || !delta.Amount.IsZero() {
		t.Fatalf("repeat cumulative delta=%+v applied=%v error=%v", delta, commissionApplied, err)
	}
	statistics, err = store.LoadDailyStatistics(ctx, signal.StrategyID, "2026-07-29", "RUB")
	if err != nil || statistics.Commissions.String() != "0.5" || statistics.TotalPnL.String() != "-0.5" || statistics.TradeCount != 0 {
		t.Fatalf("cumulative commission statistics=%+v error=%v", statistics, err)
	}
	regressed, _ := domain.NewMoney("0.4", "RUB")
	if _, _, err := store.ApplyCumulativeOrderCommission(ctx, signal.ExchangeAccountID, order.ExchangeOrderID, regressed, fixedTime.Add(5*time.Minute), "2026-07-29"); !errors.Is(err, storage.ErrConflict) {
		t.Fatalf("commission regression error=%v", err)
	}
	wrongAsset, _ := domain.NewMoney("0.6", "USD")
	if _, _, err := store.ApplyCumulativeOrderCommission(ctx, signal.ExchangeAccountID, order.ExchangeOrderID, wrongAsset, fixedTime.Add(5*time.Minute), "2026-07-29"); !errors.Is(err, storage.ErrConflict) {
		t.Fatalf("commission asset conflict error=%v", err)
	}
	wrongInstrument := execution
	wrongInstrument.ID = "execution-wrong-instrument"
	wrongInstrument.ExchangeTrade = "trade-wrong-instrument"
	wrongInstrument.InstrumentID = "another-instrument"
	wrongEntry, _, err := store.StageExecution(ctx, signal.ExchangeAccountID, wrongInstrument, fixedTime.Add(6*time.Minute), "2026-07-29")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := store.ApplyStagedExecution(ctx, wrongEntry.ID); err == nil {
		t.Fatal("execution for another instrument was accepted")
	}
	pending, err = store.ListPendingExecutions(ctx, signal.ExchangeAccountID, 10)
	if err != nil || len(pending) != 1 || pending[0].ID != wrongEntry.ID {
		t.Fatalf("wrong-instrument inbox after rollback=%+v error=%v", pending, err)
	}
}

func TestExecutionInboxApplyFailureKeepsPending(t *testing.T) {
	store := openStore(t)
	signal := registerAndCommitSignal(t, store)
	quantity, _ := domain.NewQuantity("1")
	price, _ := domain.NewPrice("100", "RUB")
	commission, _ := domain.NewMoney("0", "RUB")
	execution := domain.Execution{
		ID: "execution-orphan", OrderID: "missing-exchange-order", StrategyID: signal.StrategyID,
		InstrumentID: signal.InstrumentID, Side: domain.OrderSideBuy, Quantity: quantity,
		Price: price, Commission: commission, ExecutedAt: fixedTime, ExchangeTrade: "trade-orphan",
	}
	ctx := context.Background()
	entry, _, err := store.StageExecution(ctx, signal.ExchangeAccountID, execution, fixedTime, "2026-07-29")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := store.ApplyStagedExecution(ctx, entry.ID); !errors.Is(err, storage.ErrNotFound) {
		t.Fatalf("apply orphan error=%v", err)
	}
	pending, err := store.ListPendingExecutions(ctx, signal.ExchangeAccountID, 10)
	if err != nil || len(pending) != 1 || pending[0].ID != entry.ID {
		t.Fatalf("pending after rollback=%+v error=%v", pending, err)
	}
}

func TestExecutionHistoryCheckpointMonotonicAndIdempotent(t *testing.T) {
	store := openStore(t)
	ctx := context.Background()
	accountID := domain.ExchangeAccountID("account-history")
	if _, err := store.LoadExecutionHistoryCheckpoint(ctx, accountID, "tinvest_operations"); !errors.Is(err, storage.ErrNotFound) {
		t.Fatalf("missing checkpoint error=%v", err)
	}
	first := storage.ExecutionHistoryCheckpoint{
		ExchangeAccountID: accountID, Source: "tinvest_operations",
		CoveredThrough: fixedTime, CreatedAt: fixedTime.Add(time.Second),
	}
	if err := store.AdvanceExecutionHistoryCheckpoint(ctx, first); err != nil {
		t.Fatal(err)
	}
	// Equal watermarks are idempotent even if a retry carries another commit time.
	retry := first
	retry.CreatedAt = fixedTime.Add(2 * time.Second)
	if err := store.AdvanceExecutionHistoryCheckpoint(ctx, retry); err != nil {
		t.Fatalf("idempotent checkpoint: %v", err)
	}
	got, err := store.LoadExecutionHistoryCheckpoint(ctx, accountID, first.Source)
	if err != nil || !got.CoveredThrough.Equal(first.CoveredThrough) || !got.CreatedAt.Equal(first.CreatedAt) {
		t.Fatalf("checkpoint=%+v error=%v", got, err)
	}
	regressed := first
	regressed.CoveredThrough = fixedTime.Add(-time.Minute)
	regressed.CreatedAt = fixedTime.Add(3 * time.Second)
	if err := store.AdvanceExecutionHistoryCheckpoint(ctx, regressed); !errors.Is(err, storage.ErrConflict) {
		t.Fatalf("regression error=%v", err)
	}
	next := first
	next.CoveredThrough = fixedTime.Add(time.Hour)
	// CoveredThrough is the monotonic value; repository ordering must also
	// tolerate equal coarse database timestamps.
	next.CreatedAt = first.CreatedAt
	if err := store.AdvanceExecutionHistoryCheckpoint(ctx, next); err != nil {
		t.Fatal(err)
	}
	got, err = store.LoadExecutionHistoryCheckpoint(ctx, accountID, first.Source)
	if err != nil || !got.CoveredThrough.Equal(next.CoveredThrough) {
		t.Fatalf("advanced checkpoint=%+v error=%v", got, err)
	}
	events, err := store.ListAudit(ctx, 10)
	if err != nil || len(events) != 2 {
		t.Fatalf("checkpoint audit events=%+v error=%v", events, err)
	}
}

func TestExecutionHistoryCheckpointIsolatedByAccountAndSource(t *testing.T) {
	store := openStore(t)
	ctx := context.Background()
	checkpoints := []storage.ExecutionHistoryCheckpoint{
		{ExchangeAccountID: "account-a", Source: "tinvest_operations", CoveredThrough: fixedTime, CreatedAt: fixedTime},
		{ExchangeAccountID: "account-a", Source: "tinvest_broker_report", CoveredThrough: fixedTime.Add(time.Hour), CreatedAt: fixedTime.Add(time.Second)},
		{ExchangeAccountID: "account-b", Source: "tinvest_operations", CoveredThrough: fixedTime.Add(2 * time.Hour), CreatedAt: fixedTime.Add(2 * time.Second)},
	}
	for _, checkpoint := range checkpoints {
		if err := store.AdvanceExecutionHistoryCheckpoint(ctx, checkpoint); err != nil {
			t.Fatal(err)
		}
	}
	for _, want := range checkpoints {
		got, err := store.LoadExecutionHistoryCheckpoint(ctx, want.ExchangeAccountID, want.Source)
		if err != nil || !got.CoveredThrough.Equal(want.CoveredThrough) {
			t.Fatalf("checkpoint %s/%s=%+v error=%v", want.ExchangeAccountID, want.Source, got, err)
		}
	}
}

func TestAuditAppendOrderingAndTransactionRollback(t *testing.T) {
	store := openStore(t)
	signal := registerAndCommitSignal(t, store)
	ctx := context.Background()
	audit := storage.AuditEvent{
		ID: "audit-1", EventType: "risk.allowed", Actor: "engine",
		ScopeType: "strategy", ScopeID: "strategy-1",
		Payload: json.RawMessage(`{"reason":"ok"}`), CreatedAt: fixedTime,
	}
	if err := store.AppendAudit(ctx, audit); err != nil {
		t.Fatal(err)
	}
	quantity, _ := domain.NewQuantity("1")
	intent := storage.OrderIntent{
		ID: "intent-rollback", SignalID: signal.ID, StrategyID: signal.StrategyID,
		ExchangeAccountID: signal.ExchangeAccountID, InstrumentID: signal.InstrumentID,
		ClientOrderID: "client-rollback", Side: domain.OrderSideBuy,
		OrderType: domain.OrderTypeMarket, Quantity: quantity, Status: "created",
		PayloadChecksum: "checksum", CreatedAt: fixedTime, UpdatedAt: fixedTime,
	}
	decision := storage.RiskDecision{
		ID: "decision-rollback", SignalID: signal.ID, Decision: "allow",
		ReasonCode: "test_allowed", Payload: json.RawMessage(`{}`), CreatedAt: fixedTime,
	}
	if err := store.RecordAllowedDecisionIntent(ctx, decision, intent, audit); !errors.Is(err, storage.ErrConflict) {
		t.Fatalf("transaction error=%v", err)
	}
	if _, err := store.GetOrderIntentByClientOrderID(ctx, intent.ClientOrderID); !errors.Is(err, storage.ErrNotFound) {
		t.Fatalf("intent survived rollback: %v", err)
	}
	events, err := store.ListAudit(ctx, 10)
	if err != nil {
		t.Fatal(err)
	}
	if len(events) != 1 || events[0].ID != audit.ID {
		t.Fatalf("audit events=%+v", events)
	}
}

func TestAgentLockFailsFastAndCanBeReleased(t *testing.T) {
	path := filepath.Join(t.TempDir(), "lazytrade.db")
	ctx := context.Background()
	first, err := storesqlite.Open(ctx, path)
	if err != nil {
		t.Fatal(err)
	}
	defer first.Close()
	second, err := storesqlite.Open(ctx, path)
	if err != nil {
		t.Fatal(err)
	}
	defer second.Close()
	if err := first.Acquire(ctx, "agent-1"); err != nil {
		t.Fatal(err)
	}
	if err := second.Acquire(ctx, "agent-2"); !errors.Is(err, storage.ErrLockHeld) {
		t.Fatalf("second acquire=%v", err)
	}
	if err := first.Release(ctx); err != nil {
		t.Fatal(err)
	}
	if err := second.Acquire(ctx, "agent-2"); err != nil {
		t.Fatalf("acquire after release: %v", err)
	}
}
