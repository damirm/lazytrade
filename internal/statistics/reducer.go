package statistics

import (
	"fmt"

	"github.com/damirm/lazytrade/internal/domain"
	"github.com/shopspring/decimal"
)

// Position is the strategy-attributed position used by the P&L reducer.
// Quantity is signed: positive is long and negative is short.
type Position struct {
	Quantity     decimal.Decimal
	AveragePrice decimal.Decimal
}

type State struct {
	Components Components
	Position   Position
}

func NewState(strategyID domain.StrategyID, asset string) (State, error) {
	components, err := NewComponents(strategyID, asset)
	if err != nil {
		return State{}, err
	}
	return State{Components: components}, nil
}

// ApplyExecution returns a new state and does not mutate its input.
func ApplyExecution(state State, execution domain.Execution) (State, error) {
	if err := state.Components.Validate(); err != nil {
		return State{}, fmt.Errorf("components: %w", err)
	}
	if err := execution.Validate(); err != nil {
		return State{}, fmt.Errorf("execution: %w", err)
	}
	if execution.StrategyID != state.Components.StrategyID {
		return State{}, ErrStrategyMismatch
	}
	if execution.Price.Asset != state.Components.Asset ||
		execution.Commission.Asset != state.Components.Asset {
		return State{}, domain.ErrAssetMismatch
	}

	next := state
	next.Components.Commissions = next.Components.Commissions.Add(execution.Commission.Amount)
	qty := execution.Quantity.Value
	if execution.Side == domain.OrderSideSell {
		qty = qty.Neg()
	}
	next.Position, next.Components.Realized = applyFill(
		state.Position,
		qty,
		execution.Price.Value,
		state.Components.Realized,
	)
	return next, nil
}

func ApplyFunding(state State, funding domain.Money) (State, error) {
	if err := funding.Validate(); err != nil {
		return State{}, err
	}
	if funding.Asset != state.Components.Asset {
		return State{}, domain.ErrAssetMismatch
	}
	next := state
	next.Components.Funding = next.Components.Funding.Add(funding.Amount)
	return next, nil
}

// MarkToMarket values the current position at price. Passing complete=false
// preserves the value while making total P&L unavailable.
func MarkToMarket(state State, price domain.Price, complete bool) (State, error) {
	if err := price.Validate(); err != nil {
		return State{}, err
	}
	if price.Asset != state.Components.Asset {
		return State{}, domain.ErrAssetMismatch
	}
	next := state
	next.Components.UnrealizedComplete = complete
	if state.Position.Quantity.IsZero() {
		next.Components.Unrealized = decimal.Zero
		return next, nil
	}
	next.Components.Unrealized = price.Value.
		Sub(state.Position.AveragePrice).
		Mul(state.Position.Quantity)
	return next, nil
}

func SetFundingComplete(state State, complete bool) State {
	next := state
	next.Components.FundingComplete = complete
	return next
}

func applyFill(position Position, signedQty, price, realized decimal.Decimal) (Position, decimal.Decimal) {
	if position.Quantity.IsZero() || position.Quantity.Sign() == signedQty.Sign() {
		total := position.Quantity.Abs().Add(signedQty.Abs())
		average := price
		if !position.Quantity.IsZero() {
			average = position.AveragePrice.Mul(position.Quantity.Abs()).
				Add(price.Mul(signedQty.Abs())).
				Div(total)
		}
		return Position{Quantity: position.Quantity.Add(signedQty), AveragePrice: average}, realized
	}

	closing := decimal.Min(position.Quantity.Abs(), signedQty.Abs())
	if position.Quantity.IsPositive() {
		realized = realized.Add(price.Sub(position.AveragePrice).Mul(closing))
	} else {
		realized = realized.Add(position.AveragePrice.Sub(price).Mul(closing))
	}

	remaining := position.Quantity.Add(signedQty)
	if remaining.IsZero() {
		return Position{}, realized
	}
	if remaining.Sign() == position.Quantity.Sign() {
		return Position{Quantity: remaining, AveragePrice: position.AveragePrice}, realized
	}
	return Position{Quantity: remaining, AveragePrice: price}, realized
}
