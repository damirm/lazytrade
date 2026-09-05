package strategy_test

import (
	"context"
	"encoding/json"
	"errors"
	"testing"
	"time"

	"github.com/damirm/lazytrade/internal/domain"
	"github.com/damirm/lazytrade/internal/strategy"
	"github.com/damirm/lazytrade/internal/strategy/movingaverage"
)

func TestWorkerDeterministicSignalIDsAndDuplicatePolicy(t *testing.T) {
	run := func() (domain.Signal, *strategy.MemoryStatePort, *strategy.Worker) {
		port := strategy.NewMemoryStatePort()
		worker := newWorker(t, "one", port)
		var signals []domain.Signal
		for index, price := range []string{"3", "2", "1", "4"} {
			got, err := worker.Process(context.Background(), event(t, index, price))
			if err != nil {
				t.Fatal(err)
			}
			signals = append(signals, got...)
		}
		if len(signals) != 1 {
			t.Fatalf("signals = %d, want 1", len(signals))
		}
		return signals[0], port, worker
	}
	first, _, worker := run()
	second, _, _ := run()
	if first.ID != second.ID {
		t.Fatalf("IDs differ: %s and %s", first.ID, second.ID)
	}
	duplicate, err := worker.Process(context.Background(), event(t, 3, "4"))
	if err != nil || len(duplicate) != 0 {
		t.Fatalf("duplicate result = %v, %v", duplicate, err)
	}
	if _, err := worker.Process(context.Background(), event(t, 2, "1")); !errors.Is(err, strategy.ErrOutOfOrderEvent) {
		t.Fatalf("old cursor error = %v", err)
	}
}

func TestWorkersAreIsolated(t *testing.T) {
	port := strategy.NewMemoryStatePort()
	first := newWorker(t, "first", port)
	second := newWorker(t, "second", port)
	for index, price := range []string{"3", "2", "1", "4"} {
		if _, err := first.Process(context.Background(), event(t, index, price)); err != nil {
			t.Fatal(err)
		}
	}
	if got := len(port.Signals("first")); got != 1 {
		t.Fatalf("first signals = %d", got)
	}
	if got := len(port.Signals("second")); got != 0 {
		t.Fatalf("second signals = %d", got)
	}
	if _, exists, err := port.Load(context.Background(), "second"); err != nil || exists {
		t.Fatalf("second state exists = %v, err = %v", exists, err)
	}
	if _, err := second.Process(context.Background(), event(t, 0, "3")); err != nil {
		t.Fatal(err)
	}
	firstState, _, _ := port.Load(context.Background(), "first")
	secondState, _, _ := port.Load(context.Background(), "second")
	if string(firstState.State.Payload) == string(secondState.State.Payload) {
		t.Fatal("workers unexpectedly share state")
	}
}

func TestWorkerFailureDoesNotAffectAnotherWorker(t *testing.T) {
	port := strategy.NewMemoryStatePort()
	failing, err := strategy.NewWorker("failing", "account", "instrument", errorStrategy{}, port)
	if err != nil {
		t.Fatal(err)
	}
	healthy := newWorker(t, "healthy", port)
	if _, err := failing.Process(context.Background(), event(t, 0, "3")); err == nil {
		t.Fatal("expected failing worker error")
	}
	for index, price := range []string{"3", "2", "1", "4"} {
		if _, err := healthy.Process(context.Background(), event(t, index, price)); err != nil {
			t.Fatalf("healthy worker: %v", err)
		}
	}
	if got := len(port.Signals("healthy")); got != 1 {
		t.Fatalf("healthy signals = %d, want 1", got)
	}
	if _, exists, err := port.Load(context.Background(), "failing"); err != nil || exists {
		t.Fatalf("failed event was committed: exists=%v err=%v", exists, err)
	}
}

type errorStrategy struct{}

func (errorStrategy) Type() string { return "error" }
func (errorStrategy) RequiredData() strategy.DataRequirements {
	return strategy.DataRequirements{}
}
func (errorStrategy) InitialState() (strategy.StateEnvelope, error) {
	return strategy.StateEnvelope{StrategyType: "error", Version: 1, Payload: json.RawMessage(`{}`)}, nil
}
func (errorStrategy) OnEvent(context.Context, strategy.StateEnvelope, strategy.Input) (strategy.Result, error) {
	return strategy.Result{}, errors.New("deliberate failure")
}

func newWorker(t *testing.T, id domain.StrategyID, port *strategy.MemoryStatePort) *strategy.Worker {
	t.Helper()
	quantity, _ := domain.NewQuantity("1")
	implementation, err := movingaverage.New(movingaverage.Config{
		FastPeriod: 2, SlowPeriod: 3, Interval: time.Minute, Quantity: quantity,
	})
	if err != nil {
		t.Fatal(err)
	}
	worker, err := strategy.NewWorker(id, "account", "instrument", implementation, port)
	if err != nil {
		t.Fatal(err)
	}
	return worker
}

func event(t *testing.T, index int, closeValue string) domain.MarketEvent {
	t.Helper()
	start := time.Date(2026, 1, 1, 10, index, 0, 0, time.UTC)
	price, err := domain.NewPrice(closeValue, "RUB")
	if err != nil {
		t.Fatal(err)
	}
	volume, _ := domain.NewQuantity("10")
	return domain.MarketEvent{
		ExchangeAccountID: "account", InstrumentID: "instrument",
		Kind: domain.MarketEventCandleClose, ExchangeTime: start.Add(time.Minute), Sequence: uint64(index + 1),
		Candle: &domain.Candle{
			Start: start, End: start.Add(time.Minute), Interval: time.Minute,
			Open: price, High: price, Low: price, Close: price, Volume: volume, Complete: true,
		},
	}
}
