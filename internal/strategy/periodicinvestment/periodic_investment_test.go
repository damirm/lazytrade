package periodicinvestment

import (
	"context"
	"encoding/json"
	"errors"
	"testing"
	"time"

	"github.com/damirm/lazytrade/internal/domain"
	"github.com/damirm/lazytrade/internal/strategy"
)

func TestMonthlyOccurrenceAndCurrentPeriodOnly(t *testing.T) {
	t.Parallel()
	implementation := testStrategy(t, time.FixedZone("MSK", 3*60*60))
	state, _ := implementation.InitialState()

	for _, test := range []struct {
		name       string
		end        time.Time
		wantSignal bool
	}{
		{"before January schedule", time.Date(2026, 1, 10, 7, 59, 0, 0, time.UTC), false},
		{"January occurrence", time.Date(2026, 1, 10, 8, 0, 0, 0, time.UTC), true},
		{"later January candle", time.Date(2026, 1, 31, 20, 0, 0, 0, time.UTC), false},
		{"February occurrence", time.Date(2026, 2, 20, 12, 0, 0, 0, time.UTC), true},
	} {
		result, err := implementation.OnEvent(context.Background(), state, strategy.Input{Event: candleAt(t, test.end, time.Hour)})
		if err != nil {
			t.Fatalf("%s: %v", test.name, err)
		}
		if (len(result.Signals) == 1) != test.wantSignal {
			t.Fatalf("%s: signals = %#v", test.name, result.Signals)
		}
		if len(result.Signals) == 1 {
			draft := result.Signals[0]
			if draft.Action != domain.SignalBuy || draft.OrderType != domain.OrderTypeMarket ||
				draft.Quantity.Value.String() != "2" || draft.ReasonCode != "periodic_investment" {
				t.Fatalf("%s: draft = %#v", test.name, draft)
			}
		}
		state = result.State
	}
}

func TestDuplicateTicksAndRestoredStateDoNotRepeatOccurrence(t *testing.T) {
	t.Parallel()
	location := time.FixedZone("MSK", 3*60*60)
	first := testStrategy(t, location)
	state, _ := first.InitialState()
	end := time.Date(2026, 1, 10, 8, 0, 0, 0, time.UTC)
	result, err := first.OnEvent(context.Background(), state, strategy.Input{Event: candleAt(t, end, time.Hour)})
	if err != nil || len(result.Signals) != 1 {
		t.Fatalf("first occurrence = %#v, %v", result, err)
	}

	duplicate, err := first.OnEvent(context.Background(), result.State, strategy.Input{Event: candleAt(t, end.Add(time.Hour), time.Hour)})
	if err != nil || len(duplicate.Signals) != 0 {
		t.Fatalf("duplicate tick = %#v, %v", duplicate, err)
	}
	serialized, err := json.Marshal(result.State)
	if err != nil {
		t.Fatal(err)
	}
	var restored strategy.StateEnvelope
	if err := json.Unmarshal(serialized, &restored); err != nil {
		t.Fatal(err)
	}
	second := testStrategy(t, location)
	afterRestart, err := second.OnEvent(context.Background(), restored, strategy.Input{Event: candleAt(t, end.Add(24*time.Hour), time.Hour)})
	if err != nil || len(afterRestart.Signals) != 0 {
		t.Fatalf("restart repeated occurrence = %#v, %v", afterRestart, err)
	}
}

func TestOccurrenceUsesConfiguredTimezone(t *testing.T) {
	t.Parallel()
	location, err := time.LoadLocation("America/New_York")
	if err != nil {
		t.Fatal(err)
	}
	for _, test := range []struct {
		name   string
		before time.Time
		at     time.Time
	}{
		{"winter", time.Date(2026, 1, 10, 15, 59, 0, 0, time.UTC), time.Date(2026, 1, 10, 16, 0, 0, 0, time.UTC)},
		{"summer DST", time.Date(2026, 7, 10, 14, 59, 0, 0, time.UTC), time.Date(2026, 7, 10, 15, 0, 0, 0, time.UTC)},
	} {
		t.Run(test.name, func(t *testing.T) {
			implementation := testStrategy(t, location)
			state, _ := implementation.InitialState()
			before, err := implementation.OnEvent(context.Background(), state, strategy.Input{Event: candleAt(t, test.before, time.Hour)})
			if err != nil || len(before.Signals) != 0 {
				t.Fatalf("before schedule = %#v, %v", before, err)
			}
			at, err := implementation.OnEvent(context.Background(), before.State, strategy.Input{Event: candleAt(t, test.at, time.Hour)})
			if err != nil || len(at.Signals) != 1 {
				t.Fatalf("at schedule = %#v, %v", at, err)
			}
		})
	}
}

func TestIgnoresUnsupportedCandleEventsWithoutChangingState(t *testing.T) {
	t.Parallel()
	implementation := testStrategy(t, time.UTC)
	state, _ := implementation.InitialState()
	event := candleAt(t, time.Date(2026, 1, 20, 12, 0, 0, 0, time.UTC), time.Minute)
	result, err := implementation.OnEvent(context.Background(), state, strategy.Input{Event: event})
	if err != nil || len(result.Signals) != 0 || string(result.State.Payload) != string(state.Payload) {
		t.Fatalf("unsupported event = %#v, %v", result, err)
	}
}

func TestNewAndFactoryRejectInvalidConfiguration(t *testing.T) {
	t.Parallel()
	quantity, _ := domain.NewQuantity("2")
	valid := Config{Interval: time.Hour, Quantity: quantity, DayOfMonth: 10, TimeOfDay: "11:00", Location: time.UTC}
	for _, test := range []struct {
		name   string
		mutate func(*Config)
	}{
		{"interval", func(config *Config) { config.Interval = 0 }},
		{"quantity", func(config *Config) { config.Quantity.Value = config.Quantity.Value.Neg() }},
		{"day zero", func(config *Config) { config.DayOfMonth = 0 }},
		{"day 29", func(config *Config) { config.DayOfMonth = 29 }},
		{"time shape", func(config *Config) { config.TimeOfDay = "1:00" }},
		{"time range", func(config *Config) { config.TimeOfDay = "24:00" }},
		{"location", func(config *Config) { config.Location = nil }},
	} {
		config := valid
		test.mutate(&config)
		if _, err := New(config); err == nil {
			t.Fatalf("%s: error = nil", test.name)
		}
	}
	for _, raw := range []string{
		`{"interval":"1h","quantity":"2","day_of_month":10,"time_of_day":"11:00","location":"UTC","unknown":true}`,
		`{"interval":"1h","quantity":"2","day_of_month":10,"time_of_day":"11:00","location":"Missing/Zone"}`,
		`{"interval":"bad","quantity":"2","day_of_month":10,"time_of_day":"11:00","location":"UTC"}`,
	} {
		if _, err := (Factory{}).Build(json.RawMessage(raw)); err == nil {
			t.Fatalf("factory accepted %s", raw)
		}
	}
}

func TestInvalidAndFutureStateFailClosed(t *testing.T) {
	t.Parallel()
	implementation := testStrategy(t, time.UTC)
	event := candleAt(t, time.Date(2026, 1, 20, 12, 0, 0, 0, time.UTC), time.Hour)
	for _, test := range []struct {
		state strategy.StateEnvelope
		want  error
	}{
		{strategy.StateEnvelope{StrategyType: Type, Version: 1, Payload: json.RawMessage(`{"last_occurrence":"2026-13"}`)}, strategy.ErrInvalidState},
		{strategy.StateEnvelope{StrategyType: Type, Version: 2, Payload: json.RawMessage(`{}`)}, strategy.ErrUnsupportedVersion},
		{strategy.StateEnvelope{StrategyType: Type, Version: 1, Payload: json.RawMessage(`{"last_occurrence":"","extra":true}`)}, strategy.ErrInvalidState},
	} {
		_, err := implementation.OnEvent(context.Background(), test.state, strategy.Input{Event: event})
		if !errors.Is(err, test.want) {
			t.Fatalf("error = %v, want %v", err, test.want)
		}
	}
}

func testStrategy(t *testing.T, location *time.Location) *PeriodicInvestment {
	t.Helper()
	quantity, _ := domain.NewQuantity("2")
	implementation, err := New(Config{
		Interval: time.Hour, Quantity: quantity, DayOfMonth: 10,
		TimeOfDay: "11:00", Location: location,
	})
	if err != nil {
		t.Fatal(err)
	}
	return implementation
}

func candleAt(t *testing.T, end time.Time, interval time.Duration) domain.MarketEvent {
	t.Helper()
	price, _ := domain.NewPrice("100", "RUB")
	volume, _ := domain.NewQuantity("1")
	return domain.MarketEvent{
		ExchangeAccountID: "account", InstrumentID: "instrument",
		Kind: domain.MarketEventCandleClose, ExchangeTime: end, ReceivedTime: end, Sequence: uint64(end.UnixNano()),
		Candle: &domain.Candle{
			Start: end.Add(-interval), End: end, Interval: interval,
			Open: price, High: price, Low: price, Close: price, Volume: volume, Complete: true,
		},
	}
}
