package exchange

import (
	"context"
	"time"

	"github.com/damirm/lazytrade/internal/domain"
)

// CandleQuery describes one bounded request for historical OHLCV candles.
// From is inclusive and To is exclusive at the application boundary.
type CandleQuery struct {
	InstrumentID domain.InstrumentID
	Asset        string
	From, To     time.Time
	Interval     time.Duration
	Limit        int32
}

// HistoricalMarketData is the read-only capability required by dataset export.
type HistoricalMarketData interface {
	Instrument(context.Context, domain.InstrumentID) (domain.Instrument, error)
	Candles(context.Context, CandleQuery) ([]domain.Candle, error)
}
