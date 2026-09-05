package exchange

// Capabilities describes features implemented by an adapter. Availability for
// a particular instrument or trading session must be checked separately.
type Capabilities struct {
	MarketOrders       bool
	LimitOrders        bool
	StopOrders         bool
	OrderBook          bool
	StreamingCandles   bool
	StreamingTrades    bool
	StreamingLastPrice bool
	Sandbox            bool
}
