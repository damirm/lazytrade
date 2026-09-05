package domain

import (
	"errors"
	"fmt"
	"time"
)

type MarketEventKind uint8

const (
	MarketEventCandleOpen MarketEventKind = iota + 1
	MarketEventTrade
	MarketEventOrderBook
	MarketEventCandleClose
	MarketEventLastPrice
	MarketEventTradingStatus
)

type EventCursor struct {
	Timestamp time.Time
	Priority  uint16
	Sequence  uint64
}

type Candle struct {
	Start    time.Time
	End      time.Time
	Interval time.Duration
	Open     Price
	High     Price
	Low      Price
	Close    Price
	Volume   Quantity
	Complete bool
}

type MarketTrade struct {
	ID       string
	Price    Price
	Quantity Quantity
	Side     OrderSide
}

type OrderBookLevel struct {
	Price    Price
	Quantity Quantity
}

type OrderBook struct {
	Depth int
	Bids  []OrderBookLevel
	Asks  []OrderBookLevel
}

type TradingStatus uint8

const (
	TradingStatusUnavailable TradingStatus = iota + 1
	TradingStatusClosed
	TradingStatusOpening
	TradingStatusOpen
	TradingStatusClosing
)

type MarketEvent struct {
	ExchangeAccountID ExchangeAccountID
	InstrumentID      InstrumentID
	Kind              MarketEventKind
	ExchangeTime      time.Time
	ReceivedTime      time.Time
	Sequence          uint64
	Candle            *Candle
	Trade             *MarketTrade
	OrderBook         *OrderBook
	LastPrice         *Price
	TradingStatus     *TradingStatus
}

func (c Candle) Validate() error {
	if c.Start.IsZero() || c.End.IsZero() || !c.End.After(c.Start) {
		return errors.New("candle end must be after start")
	}
	if c.Interval <= 0 || c.End.Sub(c.Start) != c.Interval {
		return errors.New("candle interval must match start and end")
	}
	for _, field := range []struct {
		name  string
		price Price
	}{{"open", c.Open}, {"high", c.High}, {"low", c.Low}, {"close", c.Close}} {
		if err := field.price.Validate(); err != nil {
			return fmt.Errorf("%s price: %w", field.name, err)
		}
		if field.price.Asset != c.Open.Asset {
			return fmt.Errorf("%s price: %w", field.name, ErrAssetMismatch)
		}
	}
	if c.High.Value.LessThan(c.Low.Value) ||
		c.High.Value.LessThan(c.Open.Value) ||
		c.High.Value.LessThan(c.Close.Value) ||
		c.Low.Value.GreaterThan(c.Open.Value) ||
		c.Low.Value.GreaterThan(c.Close.Value) {
		return errors.New("invalid candle OHLC range")
	}
	return c.Volume.Validate()
}

func (e MarketEvent) Validate() error {
	if err := e.ExchangeAccountID.Validate(); err != nil {
		return fmt.Errorf("exchange account ID: %w", err)
	}
	if err := e.InstrumentID.Validate(); err != nil {
		return fmt.Errorf("instrument ID: %w", err)
	}
	if e.ExchangeTime.IsZero() || e.ExchangeTime.Location() != time.UTC {
		return errors.New("exchange time must be non-zero UTC")
	}
	switch e.Kind {
	case MarketEventCandleOpen, MarketEventCandleClose:
		if e.Candle == nil {
			return errors.New("candle event has no candle")
		}
		return e.Candle.Validate()
	case MarketEventTrade:
		if e.Trade == nil {
			return errors.New("trade event has no trade")
		}
		return e.Trade.Validate()
	case MarketEventOrderBook:
		if e.OrderBook == nil {
			return errors.New("order book event has no order book")
		}
		return e.OrderBook.Validate()
	case MarketEventLastPrice:
		if e.LastPrice == nil {
			return errors.New("last price event has no price")
		}
		return e.LastPrice.Validate()
	case MarketEventTradingStatus:
		if e.TradingStatus == nil ||
			*e.TradingStatus < TradingStatusUnavailable ||
			*e.TradingStatus > TradingStatusClosing {
			return errors.New("trading status event has invalid status")
		}
		return nil
	default:
		return fmt.Errorf("unsupported market event kind %d", e.Kind)
	}
}

func (t MarketTrade) Validate() error {
	if t.ID == "" {
		return errors.New("trade ID must not be empty")
	}
	if err := t.Price.Validate(); err != nil {
		return fmt.Errorf("trade price: %w", err)
	}
	if err := t.Quantity.Validate(); err != nil || !t.Quantity.Value.IsPositive() {
		return errors.New("trade quantity must be positive")
	}
	if t.Side != OrderSideBuy && t.Side != OrderSideSell {
		return errors.New("trade side is invalid")
	}
	return nil
}

func (b OrderBook) Validate() error {
	if b.Depth <= 0 {
		return errors.New("order book depth must be positive")
	}
	if len(b.Bids) > b.Depth || len(b.Asks) > b.Depth {
		return errors.New("order book contains more levels than its depth")
	}
	for _, side := range []struct {
		name   string
		levels []OrderBookLevel
	}{{name: "bid", levels: b.Bids}, {name: "ask", levels: b.Asks}} {
		for i, level := range side.levels {
			if err := level.Price.Validate(); err != nil {
				return fmt.Errorf("%s level %d price: %w", side.name, i, err)
			}
			if err := level.Quantity.Validate(); err != nil || !level.Quantity.Value.IsPositive() {
				return fmt.Errorf("%s level %d quantity must be positive", side.name, i)
			}
		}
	}
	return nil
}
