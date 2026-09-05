package domain

import (
	"errors"
	"fmt"
	"strings"
)

var ErrInvalidInstrument = errors.New("invalid instrument")

type Instrument struct {
	ID              InstrumentID
	ExchangeAccount ExchangeAccountID
	Symbol          string
	Name            string
	BaseAsset       string
	QuoteAsset      string
	SettlementAsset string
	PriceStep       Price
	QuantityStep    Quantity
	MinQuantity     Quantity
}

func (i Instrument) Validate() error {
	if err := i.ID.Validate(); err != nil {
		return fmt.Errorf("%w: id: %v", ErrInvalidInstrument, err)
	}
	if i.Symbol == "" {
		return fmt.Errorf("%w: symbol is empty", ErrInvalidInstrument)
	}
	if strings.TrimSpace(i.BaseAsset) == "" {
		return fmt.Errorf("%w: base asset is empty", ErrInvalidInstrument)
	}
	for _, field := range []struct{ name, value string }{
		{"quote asset", i.QuoteAsset},
		{"settlement asset", i.SettlementAsset},
	} {
		normalized, err := NormalizeAsset(field.value)
		if err != nil {
			return fmt.Errorf("%w: %s: %v", ErrInvalidInstrument, field.name, err)
		}
		if normalized != field.value {
			return fmt.Errorf("%w: %s is not normalized", ErrInvalidInstrument, field.name)
		}
	}
	if err := i.PriceStep.Validate(); err != nil {
		return fmt.Errorf("%w: price step: %v", ErrInvalidInstrument, err)
	}
	if i.PriceStep.Asset != i.QuoteAsset {
		return fmt.Errorf("%w: price step asset must equal quote asset", ErrInvalidInstrument)
	}
	if err := i.QuantityStep.Validate(); err != nil {
		return fmt.Errorf("%w: quantity step: %v", ErrInvalidInstrument, err)
	}
	if !i.QuantityStep.Value.IsPositive() {
		return fmt.Errorf("%w: quantity step must be positive", ErrInvalidInstrument)
	}
	return nil
}
