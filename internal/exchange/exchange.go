package exchange

import (
	"context"
	"time"

	"github.com/damirm/lazytrade/internal/domain"
)

// ExecutionHistoryProvider exposes a bounded, fully paginated view of orders
// and their immutable exchange fills. Implementations must fail rather than
// return a partial window.
type ExecutionHistoryProvider interface {
	ExecutionHistory(context.Context, ExecutionHistoryRequest) (ExecutionHistory, error)
}

type ExecutionHistoryRequest struct {
	AccountID domain.ExchangeAccountID
	From      time.Time
	To        time.Time
}

type ExecutionHistory struct {
	From     time.Time
	To       time.Time
	Orders   []RecoveredOrderSnapshot
	Complete bool
}

type RecoveredOrderSnapshot struct {
	ExchangeOrderID      domain.OrderID
	ClientOrderID        domain.ClientOrderID
	InstrumentID         domain.InstrumentID
	Side                 domain.OrderSide
	OrderType            domain.OrderType
	RequestedQuantity    domain.Quantity
	Status               domain.OrderStatus
	SubmittedAt          time.Time
	Fills                []RecoveredExecutionFill
	CumulativeCommission domain.Money
	Complete             bool
}

type RecoveredExecutionFill struct {
	TradeID    string
	Quantity   domain.Quantity
	Price      domain.Price
	ExecutedAt time.Time
}

// Exchange is the normalized boundary used by the engine. Adapter SDK and
// transport types must not cross this interface.
type Exchange interface {
	Name() string
	Capabilities() Capabilities

	Instruments(context.Context) ([]domain.Instrument, error)
	Portfolio(context.Context, domain.ExchangeAccountID) (Portfolio, error)

	SubscribeMarketData(context.Context, []Subscription) (MarketStream, error)
	SubscribeExecutions(context.Context, domain.ExchangeAccountID) (ExecutionStream, error)

	PlaceOrder(context.Context, NewOrder) (domain.Order, error)
	CancelOrder(context.Context, domain.OrderID) error
	GetOrder(context.Context, domain.OrderID) (domain.Order, error)
	GetOrderByClientID(context.Context, domain.ClientOrderID) (domain.Order, error)
	OpenOrders(context.Context, domain.ExchangeAccountID) ([]domain.Order, error)
}

// OrderContextRegistrar restores application metadata absent from exchange
// execution messages.
type OrderContextRegistrar interface {
	RegisterOrderContext(domain.OrderID, domain.StrategyID, domain.InstrumentID, domain.OrderSide)
}
