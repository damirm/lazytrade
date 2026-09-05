package statistics

import (
	"errors"
	"testing"
	"time"

	"github.com/damirm/lazytrade/internal/domain"
)

func TestReducerRealizedCommissionsFundingAndUnrealized(t *testing.T) {
	state, err := NewState("strategy-a", "RUB")
	if err != nil {
		t.Fatal(err)
	}
	state = applyTestExecution(t, state, "buy", domain.OrderSideBuy, "2", "100", "1")
	state = applyTestExecution(t, state, "sell", domain.OrderSideSell, "1", "120", "2")
	funding, _ := domain.NewMoney("-3", "RUB")
	state, err = ApplyFunding(state, funding)
	if err != nil {
		t.Fatal(err)
	}
	mark, _ := domain.NewPrice("110", "RUB")
	state, err = MarkToMarket(state, mark, true)
	if err != nil {
		t.Fatal(err)
	}

	assertDecimal(t, state.Components.Realized.String(), "20")
	assertDecimal(t, state.Components.Unrealized.String(), "10")
	assertDecimal(t, state.Components.Commissions.String(), "3")
	assertDecimal(t, state.Components.Funding.String(), "-3")
	assertDecimal(t, state.Components.RealizedNet().Amount.String(), "14")
	total, err := state.Components.Total()
	if err != nil {
		t.Fatal(err)
	}
	assertDecimal(t, total.Amount.String(), "24")
}

func TestReducerCrossesFromLongToShort(t *testing.T) {
	state, _ := NewState("strategy-a", "USD")
	state = applyTestExecution(t, state, "buy", domain.OrderSideBuy, "2", "10", "0")
	state = applyTestExecution(t, state, "sell", domain.OrderSideSell, "3", "12", "0")
	assertDecimal(t, state.Components.Realized.String(), "4")
	assertDecimal(t, state.Position.Quantity.String(), "-1")
	assertDecimal(t, state.Position.AveragePrice.String(), "12")
}

func TestIncompleteTotalPnL(t *testing.T) {
	components, _ := NewComponents("strategy-a", "RUB")
	components.UnrealizedComplete = false
	if _, err := components.Total(); !errors.Is(err, ErrIncompletePnL) {
		t.Fatalf("Total() error = %v, want ErrIncompletePnL", err)
	}
}

func TestComponentsRejectStrategyAndAssetAggregation(t *testing.T) {
	left, _ := NewComponents("strategy-a", "RUB")
	otherStrategy, _ := NewComponents("strategy-b", "RUB")
	otherAsset, _ := NewComponents("strategy-a", "USD")
	if _, err := left.Delta(otherStrategy); !errors.Is(err, ErrStrategyMismatch) {
		t.Fatalf("strategy error = %v", err)
	}
	if _, err := left.Delta(otherAsset); !errors.Is(err, domain.ErrAssetMismatch) {
		t.Fatalf("asset error = %v", err)
	}
}

func applyTestExecution(t *testing.T, state State, id string, side domain.OrderSide, quantity, price, commission string) State {
	t.Helper()
	q, _ := domain.NewQuantity(quantity)
	p, _ := domain.NewPrice(price, state.Components.Asset)
	fee, _ := domain.NewMoney(commission, state.Components.Asset)
	next, err := ApplyExecution(state, domain.Execution{
		ID: domain.ExecutionID(id), OrderID: domain.OrderID("order-" + id), StrategyID: state.Components.StrategyID,
		InstrumentID: "instrument", Side: side, Quantity: q, Price: p,
		Commission: fee, ExecutedAt: time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC),
	})
	if err != nil {
		t.Fatal(err)
	}
	return next
}

func assertDecimal(t *testing.T, got, want string) {
	t.Helper()
	if got != want {
		t.Fatalf("decimal = %s, want %s", got, want)
	}
}
