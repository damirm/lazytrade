package backtest

import (
	"context"
	"io"
	"testing"
	"time"

	"github.com/damirm/lazytrade/internal/clock"
	"github.com/damirm/lazytrade/internal/domain"
	"github.com/damirm/lazytrade/internal/risk"
	"github.com/shopspring/decimal"
)

type sliceIterator struct {
	events []domain.MarketEvent
	index  int
}

func (i *sliceIterator) Next(context.Context) (domain.MarketEvent, error) {
	if i.index == len(i.events) {
		return domain.MarketEvent{}, io.EOF
	}
	event := i.events[i.index]
	i.index++
	return event, nil
}

type scriptedProcessor struct {
	actions map[uint64]domain.SignalAction
	qty     string
}

func (p scriptedProcessor) Process(_ context.Context, event domain.MarketEvent) ([]domain.Signal, error) {
	action, ok := p.actions[event.Sequence]
	if !ok {
		return nil, nil
	}
	return []domain.Signal{{
		ID:         domain.SignalID("signal-" + decimal.NewFromInt(int64(event.Sequence)).String()),
		StrategyID: "strategy", ExchangeAccountID: "backtest", InstrumentID: "TEST",
		Action: action, OrderType: domain.OrderTypeMarket, Quantity: quantity(p.qty),
		CreatedAt: event.ExchangeTime, CausativeCursor: domain.EventCursor{
			Timestamp: event.ExchangeTime, Priority: 50, Sequence: event.Sequence,
		},
	}}, nil
}

func managed(t *testing.T, virtual *clock.VirtualClock, position, daily string) *ManagedRiskEvaluator {
	t.Helper()
	policy, err := risk.NewTradingDayPolicy("UTC", "00:00")
	if err != nil {
		t.Fatal(err)
	}
	config := risk.Config{
		StrategyID: "strategy", SettlementAsset: "USD", TradingDay: policy,
	}
	if position != "" {
		config.MaxPositionValue = &domain.Money{Amount: decimal.RequireFromString(position), Asset: "USD"}
	}
	if daily != "" {
		config.MaxDailyLoss = &risk.DailyLossLimit{
			Limit: domain.Money{Amount: decimal.RequireFromString(daily), Asset: "USD"},
			Mode:  risk.PnLTotal,
		}
	}
	result, err := NewManagedRiskEvaluator(config, virtual)
	if err != nil {
		t.Fatal(err)
	}
	return result
}

func sequencedCandle(t *testing.T, sequence uint64, start, priceValue string) domain.MarketEvent {
	event := candleEvent(t, start, priceValue, priceValue, priceValue, priceValue)
	event.Sequence = sequence
	return event
}

func TestManagedRiskRejectsProjectedPosition(t *testing.T) {
	event := sequencedCandle(t, 1, "2025-01-01T10:00:00Z", "100")
	virtual := clock.NewVirtual(time.Time{})
	evaluator := managed(t, virtual, "1000", "")
	broker := newBroker(t, "0", "0")
	report, err := (Runner{
		Iterator: &sliceIterator{events: []domain.MarketEvent{event}}, Clock: virtual,
		Strategy: scriptedProcessor{actions: map[uint64]domain.SignalAction{1: domain.SignalBuy}, qty: "11"},
		Risk:     evaluator, Broker: broker,
	}).Run(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if report.Metrics.RiskRejections != 1 || report.LastRiskReason != string(risk.ReasonMaxPositionValue) {
		t.Fatalf("unexpected risk result: %+v, reason %q", report.Metrics, report.LastRiskReason)
	}
	if len(report.Orders) != 0 {
		t.Fatalf("rejected signal created orders: %+v", report.Orders)
	}
}

func TestManagedRiskExactDailyLimitPausesAndDoesNotAutoResume(t *testing.T) {
	events := []domain.MarketEvent{
		sequencedCandle(t, 1, "2025-01-01T10:00:00Z", "100"),
		sequencedCandle(t, 2, "2025-01-01T10:01:00Z", "100"),
		sequencedCandle(t, 3, "2025-01-01T10:02:00Z", "90"),
		sequencedCandle(t, 4, "2025-01-02T10:00:00Z", "90"),
	}
	virtual := clock.NewVirtual(time.Time{})
	evaluator := managed(t, virtual, "", "10")
	broker := newBroker(t, "0", "0")
	report, err := (Runner{
		Iterator: &sliceIterator{events: events}, Clock: virtual,
		Strategy: scriptedProcessor{
			actions: map[uint64]domain.SignalAction{1: domain.SignalBuy, 2: domain.SignalSell, 4: domain.SignalBuy},
			qty:     "1",
		},
		Risk: evaluator, Broker: broker,
	}).Run(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if evaluator.Status() != risk.StatusRiskPaused {
		t.Fatalf("status %q", evaluator.Status())
	}
	if report.Metrics.RiskPauses != 1 {
		t.Fatalf("risk pauses %d, want the exact-limit transition only", report.Metrics.RiskPauses)
	}
	if report.Metrics.RiskRejections != 1 || len(report.Orders) != 2 {
		t.Fatalf("rejections=%d orders=%d", report.Metrics.RiskRejections, len(report.Orders))
	}
	if report.Metrics.RealizedPnL.Amount != "-10" || report.Metrics.ClosedTrades != 1 ||
		report.Metrics.LosingTrades != 1 || report.Metrics.GrossLoss.Amount != "10" {
		t.Fatalf("unexpected P&L metrics: %+v", report.Metrics)
	}
}
