package domain

import (
	"errors"
	"strings"
)

var ErrEmptyID = errors.New("domain ID must not be empty")

type StrategyID string
type SignalID string
type OrderID string
type ClientOrderID string
type ExecutionID string
type ExchangeAccountID string
type InstrumentID string

func ValidateID(value string) error {
	if strings.TrimSpace(value) == "" {
		return ErrEmptyID
	}
	return nil
}

func (id StrategyID) Validate() error        { return ValidateID(string(id)) }
func (id SignalID) Validate() error          { return ValidateID(string(id)) }
func (id OrderID) Validate() error           { return ValidateID(string(id)) }
func (id ClientOrderID) Validate() error     { return ValidateID(string(id)) }
func (id ExecutionID) Validate() error       { return ValidateID(string(id)) }
func (id ExchangeAccountID) Validate() error { return ValidateID(string(id)) }
func (id InstrumentID) Validate() error      { return ValidateID(string(id)) }
