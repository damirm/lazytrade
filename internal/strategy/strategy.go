package strategy

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"time"

	"github.com/damirm/lazytrade/internal/domain"
)

var (
	ErrInvalidState       = errors.New("invalid strategy state")
	ErrUnsupportedVersion = errors.New("unsupported strategy state version")
)

type StateEnvelope struct {
	StrategyType string          `json:"strategy_type"`
	Version      uint32          `json:"version"`
	Payload      json.RawMessage `json:"payload"`
}

func (s StateEnvelope) Validate(strategyType string) error {
	if s.StrategyType == "" || s.StrategyType != strategyType {
		return fmt.Errorf("%w: strategy type %q does not match %q", ErrInvalidState, s.StrategyType, strategyType)
	}
	if s.Version == 0 {
		return fmt.Errorf("%w: version must be at least 1", ErrInvalidState)
	}
	if len(s.Payload) == 0 || !json.Valid(s.Payload) {
		return fmt.Errorf("%w: payload is not valid JSON", ErrInvalidState)
	}
	decoder := json.NewDecoder(bytes.NewReader(s.Payload))
	var object map[string]json.RawMessage
	if err := decoder.Decode(&object); err != nil || object == nil {
		return fmt.Errorf("%w: payload must be a JSON object", ErrInvalidState)
	}
	return nil
}

type Input struct {
	StrategyID      domain.StrategyID
	ExchangeAccount domain.ExchangeAccountID
	InstrumentID    domain.InstrumentID
	Event           domain.MarketEvent
}

type Result struct {
	State   StateEnvelope
	Signals []domain.SignalDraft
}

type DataRequirements struct {
	CandleIntervals []time.Duration
	Trades          bool
	OrderBookDepth  int
	WarmupEvents    uint64
}

type Strategy interface {
	Type() string
	RequiredData() DataRequirements
	InitialState() (StateEnvelope, error)
	OnEvent(context.Context, StateEnvelope, Input) (Result, error)
}

func ValidateDraft(draft domain.SignalDraft) error {
	if draft.Action < domain.SignalBuy || draft.Action > domain.SignalClose {
		return errors.New("invalid signal action")
	}
	if err := draft.Quantity.Validate(); err != nil || draft.Quantity.Value.IsZero() {
		return errors.New("signal quantity must be positive")
	}
	switch draft.OrderType {
	case domain.OrderTypeMarket:
		if draft.LimitPrice != nil {
			return errors.New("market signal must not have limit price")
		}
	case domain.OrderTypeLimit:
		if draft.LimitPrice == nil {
			return errors.New("limit signal requires limit price")
		}
		if err := draft.LimitPrice.Validate(); err != nil {
			return fmt.Errorf("limit price: %w", err)
		}
	default:
		return errors.New("invalid order type")
	}
	return nil
}
