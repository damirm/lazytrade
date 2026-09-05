package exchange

import (
	"errors"
	"testing"
	"time"

	"github.com/damirm/lazytrade/internal/domain"
)

func TestSubscriptionValidation(t *testing.T) {
	tests := []struct {
		name         string
		subscription Subscription
		wantErr      bool
	}{
		{"candles", Subscription{InstrumentID: "i", Kind: SubscriptionCandles, Interval: time.Minute}, false},
		{"candles without interval", Subscription{InstrumentID: "i", Kind: SubscriptionCandles}, true},
		{"book", Subscription{InstrumentID: "i", Kind: SubscriptionOrderBook, Depth: 10}, false},
		{"book without depth", Subscription{InstrumentID: "i", Kind: SubscriptionOrderBook}, true},
		{"unknown kind", Subscription{InstrumentID: "i", Kind: 255}, true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := tt.subscription.Validate() != nil; got != tt.wantErr {
				t.Fatalf("Validate error = %v, want error %v", got, tt.wantErr)
			}
		})
	}
}

func TestErrorClassificationAndSafeFormatting(t *testing.T) {
	cause := errors.New("transport")
	err := &Error{
		Operation: "place order", Category: ErrorUnknownOutcome,
		Outcome: OutcomeUnknown, Code: "deadline", Message: "outcome unknown", Cause: cause,
	}
	if !IsCategory(err, ErrorUnknownOutcome) {
		t.Fatal("unknown-outcome category not detected")
	}
	if !errors.Is(err, cause) {
		t.Fatal("cause is not available through errors.Is")
	}
	if got, want := err.Error(), "place order: outcome unknown (deadline)"; got != want {
		t.Fatalf("Error() = %q, want %q", got, want)
	}
}

func TestNewOrderValidation(t *testing.T) {
	quantity, _ := domain.NewQuantity("1")
	price, _ := domain.NewPrice("100", "RUB")
	valid := NewOrder{
		ClientOrderID: "client", StrategyID: "strategy", ExchangeAccountID: "account",
		InstrumentID: "instrument", Side: domain.OrderSideBuy, Type: domain.OrderTypeLimit,
		Quantity: quantity, LimitPrice: &price,
	}
	if err := valid.Validate(); err != nil {
		t.Fatalf("valid order: %v", err)
	}
	valid.Type = domain.OrderTypeMarket
	if err := valid.Validate(); err == nil {
		t.Fatal("market order with limit price accepted")
	}
}
