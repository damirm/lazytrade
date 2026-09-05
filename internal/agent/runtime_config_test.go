package agent

import (
	"testing"
	"time"

	"github.com/damirm/lazytrade/internal/domain"
	"github.com/damirm/lazytrade/internal/exchange"
	"github.com/damirm/lazytrade/internal/exchange/fake"
	"github.com/damirm/lazytrade/internal/strategy"
)

func singleTestStrategy(
	worker *strategy.Worker,
	risk SignalRisk,
	subscription exchange.Subscription,
	tradingDayKey func(time.Time) string,
) []StrategyBinding {
	return []StrategyBinding{{
		ID: "ma", InstrumentID: subscription.InstrumentID, Worker: worker, Risk: risk,
		Subscription: subscription, TradingDayKey: tradingDayKey,
	}}
}

func testStrategyBindings(
	t *testing.T,
	workers map[domain.InstrumentID]*strategy.Worker,
	strategyIDs map[domain.InstrumentID]domain.StrategyID,
	risks map[domain.StrategyID]SignalRisk,
	subscriptions []exchange.Subscription,
) []StrategyBinding {
	t.Helper()
	bindings := make([]StrategyBinding, 0, len(subscriptions))
	for _, subscription := range subscriptions {
		instrumentID := subscription.InstrumentID
		strategyID := strategyIDs[instrumentID]
		bindings = append(bindings, StrategyBinding{
			ID: strategyID, InstrumentID: instrumentID, Worker: workers[instrumentID], Risk: risks[strategyID],
			Subscription: subscription,
		})
	}
	return bindings
}

func TestNewRuntimeRejectsInconsistentStrategyBinding(t *testing.T) {
	t.Parallel()
	store, worker, _ := seedPendingSignal(t)
	_, err := NewRuntime(RuntimeConfig{
		Exchange: fake.New("fake", exchange.Capabilities{Sandbox: true}), Store: store,
		Strategies: []StrategyBinding{{
			ID: "ma", InstrumentID: "TEST", Worker: worker,
			Risk: &recordingRisk{decision: RiskDecision{Allowed: true}},
			Subscription: exchange.Subscription{
				InstrumentID: "OTHER", Kind: exchange.SubscriptionCandles, Interval: time.Minute,
			},
		}},
	})
	if err == nil {
		t.Fatal("NewRuntime() error = nil")
	}
}
