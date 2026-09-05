package exchange

import (
	"errors"
	"fmt"
	"time"

	"github.com/damirm/lazytrade/internal/domain"
)

type SubscriptionKind uint8

const (
	SubscriptionCandles SubscriptionKind = iota + 1
	SubscriptionOrderBook
	SubscriptionTrades
	SubscriptionLastPrice
	SubscriptionTradingStatus
)

type Subscription struct {
	InstrumentID domain.InstrumentID
	Kind         SubscriptionKind
	Interval     time.Duration
	Depth        int
}

func (s Subscription) Validate() error {
	if err := s.InstrumentID.Validate(); err != nil {
		return fmt.Errorf("instrument ID: %w", err)
	}
	switch s.Kind {
	case SubscriptionCandles:
		if s.Interval <= 0 {
			return errors.New("candle subscription requires positive interval")
		}
	case SubscriptionOrderBook:
		if s.Depth <= 0 {
			return errors.New("order book subscription requires positive depth")
		}
	case SubscriptionTrades, SubscriptionLastPrice, SubscriptionTradingStatus:
	default:
		return errors.New("invalid subscription kind")
	}
	return nil
}

type StreamState uint8

const (
	StreamConnecting StreamState = iota + 1
	StreamHealthy
	StreamDisconnected
	StreamReconnected
	StreamClosed
)

type StreamEvent struct {
	State         StreamState
	Generation    uint64
	Subscriptions []Subscription
}

type MarketStream struct {
	Events <-chan domain.MarketEvent
	Errors <-chan error
	State  <-chan StreamEvent
}

type ExecutionStream struct {
	Executions <-chan domain.Execution
	Errors     <-chan error
}

type NewOrder struct {
	ClientOrderID     domain.ClientOrderID
	StrategyID        domain.StrategyID
	ExchangeAccountID domain.ExchangeAccountID
	InstrumentID      domain.InstrumentID
	Side              domain.OrderSide
	Type              domain.OrderType
	Quantity          domain.Quantity
	LimitPrice        *domain.Price
}

func (o NewOrder) Validate() error {
	for name, err := range map[string]error{
		"client order ID":     o.ClientOrderID.Validate(),
		"strategy ID":         o.StrategyID.Validate(),
		"exchange account ID": o.ExchangeAccountID.Validate(),
		"instrument ID":       o.InstrumentID.Validate(),
	} {
		if err != nil {
			return fmt.Errorf("%s: %w", name, err)
		}
	}
	if o.Side != domain.OrderSideBuy && o.Side != domain.OrderSideSell {
		return errors.New("invalid order side")
	}
	if err := o.Quantity.Validate(); err != nil || !o.Quantity.Value.IsPositive() {
		return errors.New("order quantity must be positive")
	}
	switch o.Type {
	case domain.OrderTypeMarket:
		if o.LimitPrice != nil {
			return errors.New("market order must not have limit price")
		}
	case domain.OrderTypeLimit:
		if o.LimitPrice == nil {
			return errors.New("limit order requires limit price")
		}
		if err := o.LimitPrice.Validate(); err != nil {
			return fmt.Errorf("limit price: %w", err)
		}
	default:
		return errors.New("invalid order type")
	}
	return nil
}

type Position struct {
	InstrumentID domain.InstrumentID
	Quantity     domain.Quantity
	AveragePrice *domain.Price
}

type Portfolio struct {
	AccountID  domain.ExchangeAccountID
	Positions  []Position
	TotalValue []domain.Money
	AsOf       time.Time
}
