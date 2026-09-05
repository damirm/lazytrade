package periodicinvestment

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"regexp"
	"strconv"
	"strings"
	"time"

	"github.com/damirm/lazytrade/internal/domain"
	"github.com/damirm/lazytrade/internal/strategy"
)

const (
	Type         = "periodic_investment"
	StateVersion = uint32(1)
)

var occurrencePattern = regexp.MustCompile(`^[0-9]{4}-(0[1-9]|1[0-2])$`)

type Config struct {
	Interval   time.Duration
	Quantity   domain.Quantity
	DayOfMonth int
	TimeOfDay  string
	Location   *time.Location
}

type PeriodicInvestment struct {
	config Config
	hour   int
	minute int
}

type stateV1 struct {
	LastOccurrence string `json:"last_occurrence"`
}

func New(config Config) (*PeriodicInvestment, error) {
	if config.Interval <= 0 {
		return nil, errors.New("interval must be positive")
	}
	if err := config.Quantity.Validate(); err != nil || !config.Quantity.Value.IsPositive() {
		return nil, errors.New("quantity must be positive")
	}
	if config.DayOfMonth < 1 || config.DayOfMonth > 28 {
		return nil, errors.New("day_of_month must be between 1 and 28")
	}
	if config.Location == nil {
		return nil, errors.New("location is required")
	}
	hour, minute, err := parseTimeOfDay(config.TimeOfDay)
	if err != nil {
		return nil, err
	}
	return &PeriodicInvestment{config: config, hour: hour, minute: minute}, nil
}

func (p *PeriodicInvestment) Type() string { return Type }

func (p *PeriodicInvestment) RequiredData() strategy.DataRequirements {
	return strategy.DataRequirements{CandleIntervals: []time.Duration{p.config.Interval}}
}

func (p *PeriodicInvestment) InitialState() (strategy.StateEnvelope, error) {
	return encodeState(stateV1{})
}

func (p *PeriodicInvestment) OnEvent(
	ctx context.Context,
	envelope strategy.StateEnvelope,
	input strategy.Input,
) (strategy.Result, error) {
	if err := ctx.Err(); err != nil {
		return strategy.Result{}, err
	}
	state, err := decodeState(envelope)
	if err != nil {
		return strategy.Result{}, err
	}
	event := input.Event
	if event.Kind != domain.MarketEventCandleClose || event.Candle == nil ||
		!event.Candle.Complete || event.Candle.Interval != p.config.Interval {
		return strategy.Result{State: envelope}, nil
	}

	localEnd := event.Candle.End.In(p.config.Location)
	occurrence := fmt.Sprintf("%04d-%02d", localEnd.Year(), int(localEnd.Month()))
	scheduled := time.Date(
		localEnd.Year(), localEnd.Month(), p.config.DayOfMonth,
		p.hour, p.minute, 0, 0, p.config.Location,
	)
	if localEnd.Before(scheduled) || state.LastOccurrence == occurrence || state.LastOccurrence > occurrence {
		return strategy.Result{State: envelope}, nil
	}
	state.LastOccurrence = occurrence
	next, err := encodeState(state)
	if err != nil {
		return strategy.Result{}, err
	}
	return strategy.Result{
		State: next,
		Signals: []domain.SignalDraft{{
			Action: domain.SignalBuy, OrderType: domain.OrderTypeMarket,
			Quantity: p.config.Quantity, ReasonCode: "periodic_investment",
			Reason: "scheduled monthly investment " + occurrence,
		}},
	}, nil
}

func parseTimeOfDay(value string) (int, int, error) {
	if len(value) != 5 || value[2] != ':' {
		return 0, 0, errors.New("time_of_day must use HH:MM")
	}
	hour, hourErr := strconv.Atoi(value[:2])
	minute, minuteErr := strconv.Atoi(value[3:])
	if hourErr != nil || minuteErr != nil || hour < 0 || hour > 23 || minute < 0 || minute > 59 {
		return 0, 0, errors.New("time_of_day must use HH:MM")
	}
	return hour, minute, nil
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
	if envelope.Version != StateVersion {
		return stateV1{}, fmt.Errorf("%w: %d", strategy.ErrUnsupportedVersion, envelope.Version)
	}
	decoder := json.NewDecoder(bytes.NewReader(envelope.Payload))
	decoder.DisallowUnknownFields()
	var value stateV1
	if err := decoder.Decode(&value); err != nil {
		return stateV1{}, fmt.Errorf("%w: %v", strategy.ErrInvalidState, err)
	}
	if value.LastOccurrence != "" && !occurrencePattern.MatchString(value.LastOccurrence) {
		return stateV1{}, fmt.Errorf("%w: invalid last occurrence", strategy.ErrInvalidState)
	}
	return value, nil
}

type Factory struct{}

func (Factory) Type() string { return Type }

type rawConfig struct {
	Interval   string `json:"interval"`
	Quantity   string `json:"quantity"`
	DayOfMonth int    `json:"day_of_month"`
	TimeOfDay  string `json:"time_of_day"`
	Location   string `json:"location"`
}

func (Factory) Build(raw json.RawMessage) (strategy.Strategy, error) {
	decoder := json.NewDecoder(bytes.NewReader(raw))
	decoder.DisallowUnknownFields()
	var input rawConfig
	if err := decoder.Decode(&input); err != nil {
		return nil, fmt.Errorf("decode %s config: %w", Type, err)
	}
	var trailing json.RawMessage
	if err := decoder.Decode(&trailing); !errors.Is(err, io.EOF) {
		if err == nil {
			err = errors.New("trailing JSON value")
		}
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
	locationName := strings.TrimSpace(input.Location)
	location, err := time.LoadLocation(locationName)
	if err != nil || locationName == "" {
		return nil, fmt.Errorf("parse location %q: %w", input.Location, err)
	}
	return New(Config{
		Interval: interval, Quantity: quantity, DayOfMonth: input.DayOfMonth,
		TimeOfDay: input.TimeOfDay, Location: location,
	})
}
