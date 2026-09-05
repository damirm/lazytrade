package domain

import (
	"errors"
	"fmt"
	"time"
)

type SignalAction uint8

const (
	SignalBuy SignalAction = iota + 1
	SignalSell
	SignalClose
)

type SignalDraft struct {
	Action     SignalAction
	OrderType  OrderType
	Quantity   Quantity
	LimitPrice *Price
	ReasonCode string
	Reason     string
}

type Signal struct {
	ID                SignalID
	StrategyID        StrategyID
	ExchangeAccountID ExchangeAccountID
	InstrumentID      InstrumentID
	Action            SignalAction
	OrderType         OrderType
	Quantity          Quantity
	LimitPrice        *Price
	ReasonCode        string
	Reason            string
	CreatedAt         time.Time
	CausativeCursor   EventCursor
	Ordinal           uint16
}

func (s Signal) Validate() error {
	for _, field := range []struct {
		name string
		err  error
	}{
		{"id", s.ID.Validate()},
		{"strategy ID", s.StrategyID.Validate()},
		{"exchange account ID", s.ExchangeAccountID.Validate()},
		{"instrument ID", s.InstrumentID.Validate()},
	} {
		if field.err != nil {
			return fmt.Errorf("%s: %w", field.name, field.err)
		}
	}
	if s.Action < SignalBuy || s.Action > SignalClose {
		return errors.New("invalid signal action")
	}
	if err := validateOrderRequest(s.OrderType, s.Quantity, s.LimitPrice); err != nil {
		return err
	}
	if s.CreatedAt.IsZero() || s.CreatedAt.Location() != time.UTC {
		return errors.New("signal created time must be non-zero UTC")
	}
	return nil
}
