package movingaverage

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"time"

	"github.com/damirm/lazytrade/internal/domain"
	"github.com/damirm/lazytrade/internal/strategy"
	"github.com/shopspring/decimal"
)

const (
	Type         = "moving_average_cross"
	StateVersion = uint32(1)
)

type Config struct {
	FastPeriod int             `json:"fast_period"`
	SlowPeriod int             `json:"slow_period"`
	Interval   time.Duration   `json:"-"`
	Quantity   domain.Quantity `json:"-"`
}

func New(config Config) (*MovingAverageCross, error) {
	if config.FastPeriod <= 0 || config.SlowPeriod <= config.FastPeriod {
		return nil, errors.New("periods must satisfy 0 < fast_period < slow_period")
	}
	if config.Interval <= 0 {
		return nil, errors.New("interval must be positive")
	}
	if err := config.Quantity.Validate(); err != nil || config.Quantity.Value.IsZero() {
		return nil, errors.New("quantity must be positive")
	}
	return &MovingAverageCross{config: config}, nil
}

type MovingAverageCross struct {
	config Config
}

type stateV1 struct {
	Closes           []string `json:"closes"`
	PreviousRelation int8     `json:"previous_relation"`
	HasRelation      bool     `json:"has_relation"`
}

func (m *MovingAverageCross) Type() string { return Type }

func (m *MovingAverageCross) RequiredData() strategy.DataRequirements {
	return strategy.DataRequirements{
		CandleIntervals: []time.Duration{m.config.Interval},
		WarmupEvents:    uint64(m.config.SlowPeriod),
	}
}

func (m *MovingAverageCross) InitialState() (strategy.StateEnvelope, error) {
	return encodeState(stateV1{})
}

func (m *MovingAverageCross) OnEvent(ctx context.Context, envelope strategy.StateEnvelope, input strategy.Input) (strategy.Result, error) {
	if err := ctx.Err(); err != nil {
		return strategy.Result{}, err
	}
	state, err := decodeState(envelope)
	if err != nil {
		return strategy.Result{}, err
	}
	event := input.Event
	if event.Kind != domain.MarketEventCandleClose || event.Candle == nil || !event.Candle.Complete || event.Candle.Interval != m.config.Interval {
		return strategy.Result{State: envelope}, nil
	}
	state.Closes = append(state.Closes, event.Candle.Close.Value.String())
	if len(state.Closes) > m.config.SlowPeriod {
		state.Closes = append([]string(nil), state.Closes[len(state.Closes)-m.config.SlowPeriod:]...)
	}
	var drafts []domain.SignalDraft
	if len(state.Closes) == m.config.SlowPeriod {
		fast, err := average(state.Closes[len(state.Closes)-m.config.FastPeriod:])
		if err != nil {
			return strategy.Result{}, err
		}
		slow, err := average(state.Closes)
		if err != nil {
			return strategy.Result{}, err
		}
		relation := int8(fast.Cmp(slow))
		if state.HasRelation && state.PreviousRelation <= 0 && relation > 0 {
			drafts = append(drafts, m.draft(domain.SignalBuy, "fast_ma_crossed_above", "fast moving average crossed above slow moving average"))
		}
		if state.HasRelation && state.PreviousRelation >= 0 && relation < 0 {
			drafts = append(drafts, m.draft(domain.SignalSell, "fast_ma_crossed_below", "fast moving average crossed below slow moving average"))
		}
		state.PreviousRelation = relation
		state.HasRelation = true
	}
	next, err := encodeState(state)
	if err != nil {
		return strategy.Result{}, err
	}
	return strategy.Result{State: next, Signals: drafts}, nil
}

func (m *MovingAverageCross) draft(action domain.SignalAction, code, reason string) domain.SignalDraft {
	return domain.SignalDraft{
		Action: action, OrderType: domain.OrderTypeMarket, Quantity: m.config.Quantity,
		ReasonCode: code, Reason: reason,
	}
}

func average(values []string) (decimal.Decimal, error) {
	sum := decimal.Zero
	for _, value := range values {
		number, err := decimal.NewFromString(value)
		if err != nil {
			return decimal.Zero, fmt.Errorf("%w: invalid close value", strategy.ErrInvalidState)
		}
		sum = sum.Add(number)
	}
	return sum.Div(decimal.NewFromInt(int64(len(values)))), nil
}

func encodeState(value stateV1) (strategy.StateEnvelope, error) {
	payload, err := json.Marshal(value)
	if err != nil {
		return strategy.StateEnvelope{}, err
	}
	return strategy.StateEnvelope{StrategyType: Type, Version: StateVersion, Payload: payload}, nil
}

func decodeState(envelope strategy.StateEnvelope) (stateV1, error) {
	if err := envelope.Validate(Type); err != nil {
		return stateV1{}, err
	}
	if envelope.Version > StateVersion {
		return stateV1{}, fmt.Errorf("%w: %d", strategy.ErrUnsupportedVersion, envelope.Version)
	}
	if envelope.Version != StateVersion {
		return stateV1{}, fmt.Errorf("%w: %d", strategy.ErrUnsupportedVersion, envelope.Version)
	}
	decoder := json.NewDecoder(bytes.NewReader(envelope.Payload))
	decoder.DisallowUnknownFields()
	var value stateV1
	if err := decoder.Decode(&value); err != nil {
		return stateV1{}, fmt.Errorf("%w: %v", strategy.ErrInvalidState, err)
	}
	if value.PreviousRelation < -1 || value.PreviousRelation > 1 {
		return stateV1{}, fmt.Errorf("%w: invalid previous relation", strategy.ErrInvalidState)
	}
	for _, closeValue := range value.Closes {
		number, err := decimal.NewFromString(closeValue)
		if err != nil || !number.IsPositive() {
			return stateV1{}, fmt.Errorf("%w: invalid close value", strategy.ErrInvalidState)
		}
	}
	return value, nil
}

type Factory struct{}

func (Factory) Type() string { return Type }

type rawConfig struct {
	FastPeriod int    `json:"fast_period"`
	SlowPeriod int    `json:"slow_period"`
	Interval   string `json:"interval"`
	Quantity   string `json:"quantity"`
}

func (Factory) Build(raw json.RawMessage) (strategy.Strategy, error) {
	decoder := json.NewDecoder(bytes.NewReader(raw))
	decoder.DisallowUnknownFields()
	var input rawConfig
	if err := decoder.Decode(&input); err != nil {
		return nil, fmt.Errorf("decode %s config: %w", Type, err)
	}
	interval, err := time.ParseDuration(input.Interval)
	if err != nil {
		return nil, fmt.Errorf("parse interval: %w", err)
	}
	quantity, err := domain.NewQuantity(input.Quantity)
	if err != nil {
		return nil, fmt.Errorf("parse quantity: %w", err)
	}
	return New(Config{FastPeriod: input.FastPeriod, SlowPeriod: input.SlowPeriod, Interval: interval, Quantity: quantity})
}
