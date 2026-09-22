package exchange

import (
	"errors"
	"fmt"
	"time"

	"github.com/damirm/lazytrade/internal/domain"
)

// Execution is an exchange fill without application ownership. ClientOrderID
// may be absent; OrderID is always required. The consumer resolves ownership
// from its durable order/intent records before applying the fill.
type Execution struct {
	ID            domain.ExecutionID
	OrderID       domain.OrderID
	ClientOrderID domain.ClientOrderID
	InstrumentID  domain.InstrumentID
	Side          domain.OrderSide
	Quantity      domain.Quantity
	Price         domain.Price
	Commission    domain.Money
	ExecutedAt    time.Time
	ExchangeTrade string
}

func (e Execution) Validate() error {
	for _, field := range []struct {
		name string
		err  error
	}{
		{"ID", e.ID.Validate()},
		{"order ID", e.OrderID.Validate()},
		{"instrument ID", e.InstrumentID.Validate()},
	} {
		if field.err != nil {
			return fmt.Errorf("execution %s: %w", field.name, field.err)
		}
	}
	if e.ClientOrderID != "" {
		if err := e.ClientOrderID.Validate(); err != nil {
			return fmt.Errorf("execution client order ID: %w", err)
		}
	}
	if e.Side != domain.OrderSideBuy && e.Side != domain.OrderSideSell {
		return errors.New("invalid execution side")
	}
	if err := e.Quantity.Validate(); err != nil || !e.Quantity.Value.IsPositive() {
		return errors.New("execution quantity must be positive")
	}
	if err := e.Price.Validate(); err != nil {
		return fmt.Errorf("execution price: %w", err)
	}
	if err := e.Commission.Validate(); err != nil {
		return fmt.Errorf("execution commission: %w", err)
	}
	if e.Commission.Amount.IsNegative() {
		return errors.New("execution commission must not be negative")
	}
	if e.Commission.Asset != e.Price.Asset {
		return domain.ErrAssetMismatch
	}
	if e.ExecutedAt.IsZero() || e.ExecutedAt.Location() != time.UTC {
		return errors.New("execution time must be non-zero UTC")
	}
	return nil
}

// Attribute binds a fill to an owner already established by the consumer.
func (e Execution) Attribute(strategyID domain.StrategyID) (domain.Execution, error) {
	if e.ClientOrderID != "" {
		if err := e.ClientOrderID.Validate(); err != nil {
			return domain.Execution{}, fmt.Errorf("execution client order ID: %w", err)
		}
	}
	fill := domain.Execution{
		ID: e.ID, OrderID: e.OrderID, StrategyID: strategyID,
		InstrumentID: e.InstrumentID, Side: e.Side, Quantity: e.Quantity,
		Price: e.Price, Commission: e.Commission, ExecutedAt: e.ExecutedAt,
		ExchangeTrade: e.ExchangeTrade,
	}
	if err := fill.Validate(); err != nil {
		return domain.Execution{}, err
	}
	return fill, nil
}
