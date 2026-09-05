package domain

import (
	"errors"
	"fmt"
	"time"
)

type OrderSide uint8

const (
	OrderSideBuy OrderSide = iota + 1
	OrderSideSell
)

type OrderType uint8

const (
	OrderTypeMarket OrderType = iota + 1
	OrderTypeLimit
)

type OrderStatus uint8

const (
	OrderStatusPending OrderStatus = iota + 1
	OrderStatusAccepted
	OrderStatusPartiallyFilled
	OrderStatusFilled
	OrderStatusCancelled
	OrderStatusRejected
	OrderStatusUnknown
)

type Order struct {
	ID                OrderID
	ClientOrderID     ClientOrderID
	StrategyID        StrategyID
	ExchangeAccountID ExchangeAccountID
	InstrumentID      InstrumentID
	Side              OrderSide
	Type              OrderType
	Status            OrderStatus
	Quantity          Quantity
	FilledQuantity    Quantity
	LimitPrice        *Price
	SubmittedAt       time.Time
	UpdatedAt         time.Time
}

type Execution struct {
	ID            ExecutionID
	OrderID       OrderID
	StrategyID    StrategyID
	InstrumentID  InstrumentID
	Side          OrderSide
	Quantity      Quantity
	Price         Price
	Commission    Money
	ExecutedAt    time.Time
	ExchangeTrade string
}

func (o Order) Validate() error {
	for _, field := range []struct {
		name string
		err  error
	}{
		{"id", o.ID.Validate()},
		{"client order ID", o.ClientOrderID.Validate()},
		{"strategy ID", o.StrategyID.Validate()},
		{"exchange account ID", o.ExchangeAccountID.Validate()},
		{"instrument ID", o.InstrumentID.Validate()},
	} {
		if field.err != nil {
			return fmt.Errorf("%s: %w", field.name, field.err)
		}
	}
	if o.Side != OrderSideBuy && o.Side != OrderSideSell {
		return errors.New("invalid order side")
	}
	if o.Status < OrderStatusPending || o.Status > OrderStatusUnknown {
		return errors.New("invalid order status")
	}
	if err := validateOrderRequest(o.Type, o.Quantity, o.LimitPrice); err != nil {
		return err
	}
	if err := o.FilledQuantity.Validate(); err != nil {
		return fmt.Errorf("filled quantity: %w", err)
	}
	if o.FilledQuantity.Value.GreaterThan(o.Quantity.Value) {
		return errors.New("filled quantity exceeds order quantity")
	}
	if o.SubmittedAt.IsZero() || o.SubmittedAt.Location() != time.UTC {
		return errors.New("submitted time must be non-zero UTC")
	}
	return nil
}

func (e Execution) Validate() error {
	for _, field := range []struct {
		name string
		err  error
	}{
		{"id", e.ID.Validate()},
		{"order ID", e.OrderID.Validate()},
		{"strategy ID", e.StrategyID.Validate()},
		{"instrument ID", e.InstrumentID.Validate()},
	} {
		if field.err != nil {
			return fmt.Errorf("%s: %w", field.name, field.err)
		}
	}
	if e.Side != OrderSideBuy && e.Side != OrderSideSell {
		return errors.New("invalid execution side")
	}
	if err := e.Quantity.Validate(); err != nil || e.Quantity.Value.IsZero() {
		return errors.New("execution quantity must be positive")
	}
	if err := e.Price.Validate(); err != nil {
		return fmt.Errorf("execution price: %w", err)
	}
	if err := e.Commission.Validate(); err != nil {
		return fmt.Errorf("commission: %w", err)
	}
	if e.Commission.Amount.IsNegative() {
		return errors.New("commission must not be negative")
	}
	if e.Commission.Asset != e.Price.Asset {
		return ErrAssetMismatch
	}
	if e.ExecutedAt.IsZero() || e.ExecutedAt.Location() != time.UTC {
		return errors.New("executed time must be non-zero UTC")
	}
	return nil
}

func validateOrderRequest(orderType OrderType, quantity Quantity, limitPrice *Price) error {
	if orderType != OrderTypeMarket && orderType != OrderTypeLimit {
		return errors.New("invalid order type")
	}
	if err := quantity.Validate(); err != nil || quantity.Value.IsZero() {
		return errors.New("order quantity must be positive")
	}
	if orderType == OrderTypeLimit {
		if limitPrice == nil {
			return errors.New("limit order requires limit price")
		}
		return limitPrice.Validate()
	}
	if limitPrice != nil {
		return errors.New("market order must not have limit price")
	}
	return nil
}
