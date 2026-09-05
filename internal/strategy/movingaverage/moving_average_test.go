package movingaverage

import (
	"context"
	"encoding/json"
	"errors"
	"testing"
	"time"

	"github.com/damirm/lazytrade/internal/domain"
	"github.com/damirm/lazytrade/internal/strategy"
)

func TestNoSignalBeforeWarmupAndCrosses(t *testing.T) {
	implementation := newTestStrategy(t)
	state, err := implementation.InitialState()
	if err != nil {
		t.Fatal(err)
	}
	prices := []string{"3", "2", "1", "4", "5", "1"}
	var actions []domain.SignalAction
	for index, price := range prices {
		result, err := implementation.OnEvent(context.Background(), state, strategy.Input{Event: candleEvent(t, index, price)})
		if err != nil {
			t.Fatal(err)
		}
		if index < 3 && len(result.Signals) != 0 {
			t.Fatalf("signal before a previous warmed relation at index %d", index)
		}
		for _, signal := range result.Signals {
			actions = append(actions, signal.Action)
		}
		state = result.State
	}
	if len(actions) != 2 || actions[0] != domain.SignalBuy || actions[1] != domain.SignalSell {
		t.Fatalf("actions = %v, want [buy sell]", actions)
	}
}

func TestStateSerializationRestoreIsDeterministic(t *testing.T) {
	first := newTestStrategy(t)
	state, _ := first.InitialState()
	for index, price := range []string{"3", "2", "1"} {
		result, err := first.OnEvent(context.Background(), state, strategy.Input{Event: candleEvent(t, index, price)})
		if err != nil {
			t.Fatal(err)
		}
		state = result.State
	}
	serialized, err := json.Marshal(state)
	if err != nil {
		t.Fatal(err)
	}
	var restored strategy.StateEnvelope
	if err := json.Unmarshal(serialized, &restored); err != nil {
		t.Fatal(err)
	}
	second := newTestStrategy(t)
	left, err := first.OnEvent(context.Background(), state, strategy.Input{Event: candleEvent(t, 4, "4")})
	if err != nil {
		t.Fatal(err)
	}
	right, err := second.OnEvent(context.Background(), restored, strategy.Input{Event: candleEvent(t, 4, "4")})
	if err != nil {
		t.Fatal(err)
	}
	if string(left.State.Payload) != string(right.State.Payload) || len(left.Signals) != len(right.Signals) || left.Signals[0].Action != right.Signals[0].Action {
		t.Fatalf("restored result differs: %#v vs %#v", left, right)
	}
}

func TestCorruptedAndFutureStateFailSafe(t *testing.T) {
	implementation := newTestStrategy(t)
	tests := []struct {
		name  string
		state strategy.StateEnvelope
		want  error
	}{
		{"corrupted", strategy.StateEnvelope{StrategyType: Type, Version: 1, Payload: json.RawMessage(`{"closes":[`)}, strategy.ErrInvalidState},
		{"future", strategy.StateEnvelope{StrategyType: Type, Version: 2, Payload: json.RawMessage(`{}`)}, strategy.ErrUnsupportedVersion},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			_, err := implementation.OnEvent(context.Background(), test.state, strategy.Input{Event: candleEvent(t, 1, "1")})
			if !errors.Is(err, test.want) {
				t.Fatalf("error = %v, want %v", err, test.want)
			}
		})
	}
}

func newTestStrategy(t *testing.T) *MovingAverageCross {
	t.Helper()
	quantity, _ := domain.NewQuantity("1")
	value, err := New(Config{FastPeriod: 2, SlowPeriod: 3, Interval: time.Minute, Quantity: quantity})
	if err != nil {
		t.Fatal(err)
	}
	return value
}

func candleEvent(t *testing.T, index int, closeValue string) domain.MarketEvent {
	t.Helper()
	start := time.Date(2026, 1, 1, 10, index, 0, 0, time.UTC)
	price := func(value string) domain.Price {
		result, err := domain.NewPrice(value, "RUB")
		if err != nil {
			t.Fatal(err)
		}
		return result
	}
	closePrice := price(closeValue)
	volume, _ := domain.NewQuantity("10")
	return domain.MarketEvent{
		ExchangeAccountID: "account", InstrumentID: "instrument",
		Kind: domain.MarketEventCandleClose, ExchangeTime: start.Add(time.Minute), Sequence: uint64(index + 1),
		Candle: &domain.Candle{
			Start: start, End: start.Add(time.Minute), Interval: time.Minute,
			Open: closePrice, High: closePrice, Low: closePrice, Close: closePrice,
			Volume: volume, Complete: true,
		},
	}
}
