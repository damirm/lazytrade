package domain

import (
	"testing"
	"time"
)

func TestCandleValidation(t *testing.T) {
	price := func(value string) Price {
		p, err := NewPrice(value, "RUB")
		if err != nil {
			t.Fatal(err)
		}
		return p
	}
	volume, _ := NewQuantity("100")
	start := time.Date(2026, 1, 1, 10, 0, 0, 0, time.UTC)

	tests := []struct {
		name   string
		mutate func(*Candle)
		ok     bool
	}{
		{name: "valid", ok: true},
		{name: "high below close", mutate: func(c *Candle) { c.High = price("99") }},
		{name: "interval mismatch", mutate: func(c *Candle) { c.Interval = 2 * time.Minute }},
		{name: "asset mismatch", mutate: func(c *Candle) { c.Close, _ = NewPrice("101", "USD") }},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			candle := Candle{
				Start: start, End: start.Add(time.Minute), Interval: time.Minute,
				Open: price("100"), High: price("102"), Low: price("98"), Close: price("101"),
				Volume: volume, Complete: true,
			}
			if tt.mutate != nil {
				tt.mutate(&candle)
			}
			if got := candle.Validate() == nil; got != tt.ok {
				t.Fatalf("Validate success = %v, want %v", got, tt.ok)
			}
		})
	}
}

func TestOrderValidation(t *testing.T) {
	quantity, _ := NewQuantity("1")
	price, _ := NewPrice("100", "RUB")
	now := time.Date(2026, 1, 1, 10, 0, 0, 0, time.UTC)
	base := Order{
		ID: "order", ClientOrderID: "client", StrategyID: "strategy",
		ExchangeAccountID: "account", InstrumentID: "instrument",
		Side: OrderSideBuy, Type: OrderTypeLimit, Status: OrderStatusAccepted,
		Quantity: quantity, LimitPrice: &price, SubmittedAt: now,
	}
	tests := []struct {
		name   string
		mutate func(*Order)
		ok     bool
	}{
		{name: "valid limit", ok: true},
		{name: "limit without price", mutate: func(o *Order) { o.LimitPrice = nil }},
		{name: "market with price", mutate: func(o *Order) { o.Type = OrderTypeMarket }},
		{name: "empty ID", mutate: func(o *Order) { o.ID = "" }},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			order := base
			if tt.mutate != nil {
				tt.mutate(&order)
			}
			if got := order.Validate() == nil; got != tt.ok {
				t.Fatalf("Validate success = %v, want %v", got, tt.ok)
			}
		})
	}
}

func TestMarketEventPayloadValidation(t *testing.T) {
	price, _ := NewPrice("100", "RUB")
	quantity, _ := NewQuantity("2")
	now := time.Date(2026, 1, 1, 10, 0, 0, 0, time.UTC)

	tests := []struct {
		name  string
		event MarketEvent
		ok    bool
	}{
		{
			name: "trade",
			event: MarketEvent{
				Kind: MarketEventTrade,
				Trade: &MarketTrade{
					ID: "trade-1", Price: price, Quantity: quantity, Side: OrderSideBuy,
				},
			},
			ok: true,
		},
		{
			name: "order book",
			event: MarketEvent{
				Kind: MarketEventOrderBook,
				OrderBook: &OrderBook{
					Depth: 1,
					Bids:  []OrderBookLevel{{Price: price, Quantity: quantity}},
					Asks:  []OrderBookLevel{{Price: price, Quantity: quantity}},
				},
			},
			ok: true,
		},
		{name: "trade without payload", event: MarketEvent{Kind: MarketEventTrade}},
		{
			name: "order book exceeds depth",
			event: MarketEvent{
				Kind: MarketEventOrderBook,
				OrderBook: &OrderBook{
					Depth: 1,
					Bids: []OrderBookLevel{
						{Price: price, Quantity: quantity},
						{Price: price, Quantity: quantity},
					},
				},
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			event := tt.event
			event.ExchangeAccountID = "account"
			event.InstrumentID = "instrument"
			event.ExchangeTime = now
			if got := event.Validate() == nil; got != tt.ok {
				t.Fatalf("Validate success = %v, want %v", got, tt.ok)
			}
		})
	}
}
