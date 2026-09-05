package statistics

import (
	"errors"
	"fmt"

	"github.com/damirm/lazytrade/internal/domain"
	"github.com/shopspring/decimal"
)

var (
	ErrStrategyMismatch = errors.New("strategy mismatch")
	ErrIncompletePnL    = errors.New("P&L is incomplete")
)

// Components contains cumulative P&L components for one strategy and asset.
// Realized and Unrealized are gross; commissions are subtracted and funding is
// signed (a receipt is positive, a payment is negative).
type Components struct {
	StrategyID         domain.StrategyID
	Asset              string
	Realized           decimal.Decimal
	Unrealized         decimal.Decimal
	Commissions        decimal.Decimal
	Funding            decimal.Decimal
	UnrealizedComplete bool
	FundingComplete    bool
}

func NewComponents(strategyID domain.StrategyID, asset string) (Components, error) {
	if err := strategyID.Validate(); err != nil {
		return Components{}, fmt.Errorf("strategy ID: %w", err)
	}
	normalized, err := domain.NormalizeAsset(asset)
	if err != nil {
		return Components{}, err
	}
	return Components{
		StrategyID:         strategyID,
		Asset:              normalized,
		UnrealizedComplete: true,
		FundingComplete:    true,
	}, nil
}

func (c Components) Validate() error {
	if err := c.StrategyID.Validate(); err != nil {
		return fmt.Errorf("strategy ID: %w", err)
	}
	normalized, err := domain.NormalizeAsset(c.Asset)
	if err != nil {
		return err
	}
	if normalized != c.Asset {
		return fmt.Errorf("asset is not normalized: %q", c.Asset)
	}
	if c.Commissions.IsNegative() {
		return errors.New("commissions must not be negative")
	}
	return nil
}

func (c Components) RealizedNet() domain.Money {
	return domain.Money{
		Amount: c.Realized.Sub(c.Commissions).Add(c.Funding),
		Asset:  c.Asset,
	}
}

func (c Components) Total() (domain.Money, error) {
	if !c.UnrealizedComplete || !c.FundingComplete {
		return domain.Money{}, ErrIncompletePnL
	}
	return domain.Money{
		Amount: c.Realized.Add(c.Unrealized).Sub(c.Commissions).Add(c.Funding),
		Asset:  c.Asset,
	}, nil
}

// Delta returns current minus baseline without aggregating different
// strategies or assets.
func (c Components) Delta(baseline Components) (Components, error) {
	if c.StrategyID != baseline.StrategyID {
		return Components{}, ErrStrategyMismatch
	}
	if c.Asset != baseline.Asset {
		return Components{}, domain.ErrAssetMismatch
	}
	return Components{
		StrategyID:         c.StrategyID,
		Asset:              c.Asset,
		Realized:           c.Realized.Sub(baseline.Realized),
		Unrealized:         c.Unrealized.Sub(baseline.Unrealized),
		Commissions:        c.Commissions.Sub(baseline.Commissions),
		Funding:            c.Funding.Sub(baseline.Funding),
		UnrealizedComplete: c.UnrealizedComplete && baseline.UnrealizedComplete,
		FundingComplete:    c.FundingComplete && baseline.FundingComplete,
	}, nil
}
