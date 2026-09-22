package exchange

import (
	"strings"
	"testing"
)

func TestNewOrderValidationHasStableFieldOrder(t *testing.T) {
	for i := 0; i < 100; i++ {
		order := NewOrder{}
		for _, test := range []struct {
			field string
			fix   func()
		}{
			{"client order ID", func() { order.ClientOrderID = "client" }},
			{"strategy ID", func() { order.StrategyID = "strategy" }},
			{"exchange account ID", func() { order.ExchangeAccountID = "account" }},
			{"instrument ID", func() { order.InstrumentID = "instrument" }},
		} {
			if err := order.Validate(); err == nil || !strings.HasPrefix(err.Error(), test.field+":") {
				t.Fatalf("first error = %v, want %s", err, test.field)
			}
			test.fix()
		}
	}
}
