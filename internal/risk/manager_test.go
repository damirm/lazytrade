package risk

import (
	"errors"
	"testing"
	"time"

	appclock "github.com/damirm/lazytrade/internal/clock"
	"github.com/damirm/lazytrade/internal/domain"
	"github.com/damirm/lazytrade/internal/statistics"
	"github.com/shopspring/decimal"
)

func TestDailyLossPausesExactlyAtBoundary(t *testing.T) {
	manager, snapshot := testManagerAndSnapshot(t, PnLTotal)
	snapshot.DailyPnL.Realized = decimal.NewFromInt(-100)
	decision := manager.ObservePnL(snapshot)
	assertDecision(t, decision, DecisionPause, ReasonMaxDailyLoss, StatusRiskPaused)
}

func TestDailyLossAboveBoundaryAllows(t *testing.T) {
	manager, snapshot := testManagerAndSnapshot(t, PnLRealized)
	snapshot.DailyPnL.Realized = decimal.RequireFromString("-99.999")
	decision := manager.ObservePnL(snapshot)
	assertDecision(t, decision, DecisionAllow, "", StatusRunning)
}

func TestIncompleteTotalPnLPausesFailSafe(t *testing.T) {
	manager, snapshot := testManagerAndSnapshot(t, PnLTotal)
	snapshot.DailyPnL.UnrealizedComplete = false
	decision := manager.ObservePnL(snapshot)
	assertDecision(t, decision, DecisionPause, ReasonIncompletePnL, StatusRiskPaused)
}

func TestRealizedModeDoesNotRequireUnrealized(t *testing.T) {
	manager, snapshot := testManagerAndSnapshot(t, PnLRealized)
	snapshot.DailyPnL.UnrealizedComplete = false
	decision := manager.ObservePnL(snapshot)
	assertDecision(t, decision, DecisionAllow, "", StatusRunning)
}

func TestAssetMismatchPausesFailSafe(t *testing.T) {
	manager, snapshot := testManagerAndSnapshot(t, PnLTotal)
	snapshot.DailyPnL.Asset = "USD"
	decision := manager.ObservePnL(snapshot)
	assertDecision(t, decision, DecisionPause, ReasonAssetMismatch, StatusRiskPaused)
}

func TestStrategyIsolationByID(t *testing.T) {
	manager, snapshot := testManagerAndSnapshot(t, PnLTotal)
	snapshot.StrategyID = "strategy-b"
	decision := manager.ObservePnL(snapshot)
	assertDecision(t, decision, DecisionPause, ReasonAssetMismatch, StatusRiskPaused)
}

func TestMaxPositionValue(t *testing.T) {
	manager, snapshot := testManagerAndSnapshot(t, PnLTotal)
	snapshot.MarkPrice, _ = domain.NewPrice("10", "RUB")
	signal := testSignal(t, "1")

	// Exactly 100 RUB is allowed.
	snapshot.SignedPosition = decimal.NewFromInt(9)
	assertDecision(t, manager.Evaluate(signal, snapshot), DecisionAllow, "", StatusRunning)

	// 110 RUB exceeds the limit.
	snapshot.SignedPosition = decimal.NewFromInt(10)
	assertDecision(t, manager.Evaluate(signal, snapshot), DecisionReject, ReasonMaxPositionValue, StatusRunning)
}

func TestRiskPauseNeverAutoResumes(t *testing.T) {
	status, err := Pause(StatusRunning)
	if err != nil {
		t.Fatal(err)
	}
	// A day change has no transition attached to it.
	policy, _ := NewTradingDayPolicy("UTC", "00:00")
	oldDay := policy.At(time.Date(2026, 1, 1, 23, 59, 0, 0, time.UTC))
	newDay := policy.At(time.Date(2026, 1, 2, 0, 0, 0, 0, time.UTC))
	if oldDay.Key == newDay.Key {
		t.Fatal("expected day rollover")
	}
	if status != StatusRiskPaused {
		t.Fatal("day rollover changed status")
	}
	status, err = Resume(status, false)
	if !errors.Is(err, ErrResumeConfirmation) || status != StatusRiskPaused {
		t.Fatalf("unconfirmed Resume = %s, %v", status, err)
	}
	status, err = Resume(status, true)
	if err != nil || status != StatusRunning {
		t.Fatalf("confirmed Resume = %s, %v", status, err)
	}
}

func TestTradingDayTimezoneAndReset(t *testing.T) {
	policy, err := NewTradingDayPolicy("Europe/Moscow", "07:00")
	if err != nil {
		t.Fatal(err)
	}
	before := policy.At(time.Date(2026, 1, 2, 3, 59, 59, 0, time.UTC)) // 06:59:59 MSK
	at := policy.At(time.Date(2026, 1, 2, 4, 0, 0, 0, time.UTC))       // 07:00 MSK
	if before.Key != "2026-01-01" || at.Key != "2026-01-02" {
		t.Fatalf("keys = %s, %s", before.Key, at.Key)
	}
	if !at.StartsAt.Equal(time.Date(2026, 1, 2, 4, 0, 0, 0, time.UTC)) {
		t.Fatalf("start = %v", at.StartsAt)
	}
}

func testManagerAndSnapshot(t *testing.T, mode PnLMode) (*Manager, Snapshot) {
	t.Helper()
	now := time.Date(2026, 1, 1, 12, 0, 0, 0, time.UTC)
	policy, _ := NewTradingDayPolicy("UTC", "00:00")
	maxPosition, _ := domain.NewMoney("100", "RUB")
	dailyLimit, _ := domain.NewMoney("100", "RUB")
	manager, err := NewManager(Config{
		StrategyID: "strategy-a", SettlementAsset: "RUB",
		MaxPositionValue: &maxPosition,
		MaxDailyLoss:     &DailyLossLimit{Limit: dailyLimit, Mode: mode},
		TradingDay:       policy,
	}, appclock.NewFixed(now))
	if err != nil {
		t.Fatal(err)
	}
	pnl, _ := statistics.NewComponents("strategy-a", "RUB")
	mark, _ := domain.NewPrice("1", "RUB")
	return manager, Snapshot{
		StrategyID: "strategy-a", Status: StatusRunning, TradingDayKey: "2026-01-01",
		MarkPrice: mark, DailyPnL: pnl,
	}
}

func testSignal(t *testing.T, quantity string) domain.Signal {
	t.Helper()
	q, _ := domain.NewQuantity(quantity)
	return domain.Signal{
		ID: "signal", StrategyID: "strategy-a", ExchangeAccountID: "exchange",
		InstrumentID: "instrument", Action: domain.SignalBuy, OrderType: domain.OrderTypeMarket,
		Quantity: q, CreatedAt: time.Date(2026, 1, 1, 12, 0, 0, 0, time.UTC),
	}
}

func assertDecision(t *testing.T, decision Decision, kind DecisionKind, code ReasonCode, status StrategyStatus) {
	t.Helper()
	if decision.Kind != kind || decision.ReasonCode != code || decision.EffectiveStatus != status {
		t.Fatalf("decision = %#v; want kind=%d code=%q status=%q", decision, kind, code, status)
	}
}
