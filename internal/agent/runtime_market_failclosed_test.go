package agent

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/damirm/lazytrade/internal/domain"
	"github.com/damirm/lazytrade/internal/exchange"
	"github.com/damirm/lazytrade/internal/exchange/fake"
)

type controlledMarketExchange struct {
	exchange.Exchange
	events         chan domain.MarketEvent
	errors         chan error
	subscribeCalls int
}

func (e *controlledMarketExchange) SubscribeMarketData(context.Context, []exchange.Subscription) (exchange.MarketStream, error) {
	e.subscribeCalls++
	return exchange.MarketStream{Events: e.events, Errors: e.errors}, nil
}

func TestMultiStrategyRuntimeFailsClosedWhenMarketStreamBreaks(t *testing.T) {
	for _, test := range []struct {
		name    string
		breakIt func(chan domain.MarketEvent, chan error)
		want    string
	}{
		{"reported error", func(_ chan domain.MarketEvent, errs chan error) { errs <- errors.New("transport failed") }, "market data stream: transport failed"},
		{"events closed", func(events chan domain.MarketEvent, _ chan error) { close(events) }, "market data stream closed"},
		{"errors closed", func(_ chan domain.MarketEvent, errs chan error) { close(errs) }, "market data error stream closed"},
		{"buffered error and closed events", func(events chan domain.MarketEvent, errs chan error) {
			errs <- errors.New("terminal cause")
			close(events)
			close(errs)
		}, "market data stream: terminal cause"},
	} {
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()
			store, workers, strategyIDs, subscriptions, _ := seedTwoPendingSignals(t)
			adapter := &controlledMarketExchange{
				Exchange: fake.New("fake", exchange.Capabilities{StreamingCandles: true, Sandbox: true}),
				events:   make(chan domain.MarketEvent), errors: make(chan error, 1),
			}
			ready := make(chan struct{}, 1)
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			done := make(chan error, 1)
			risks := map[domain.StrategyID]SignalRisk{"ma-a": &multiRecoveryRisk{}, "ma-b": &multiRecoveryRisk{}}
			go func() {
				done <- (Runtime{
					Exchange: adapter, Strategies: testStrategyBindings(t, workers, strategyIDs, risks, subscriptions),
					Intents: store, Lifecycle: store, Ready: ready,
				}).Run(ctx)
			}()
			select {
			case <-ready:
			case err := <-done:
				t.Fatalf("runtime stopped before ready: %v", err)
			case <-time.After(2 * time.Second):
				t.Fatal("runtime did not become ready")
			}
			test.breakIt(adapter.events, adapter.errors)
			select {
			case err := <-done:
				if err == nil || !strings.Contains(err.Error(), test.want) {
					t.Fatalf("Run error = %v, want %q", err, test.want)
				}
			case <-time.After(2 * time.Second):
				t.Fatal("runtime did not fail closed")
			}
			if adapter.subscribeCalls != 1 {
				t.Fatalf("subscribe calls = %d, want 1", adapter.subscribeCalls)
			}
			for _, id := range strategyIDs {
				lifecycle, err := store.LoadStrategyLifecycle(context.Background(), id)
				if err != nil || lifecycle.Status != RuntimeStatusBlocked {
					t.Fatalf("strategy %s lifecycle = %+v, error = %v", id, lifecycle, err)
				}
			}
		})
	}
}
