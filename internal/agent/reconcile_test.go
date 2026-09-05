package agent

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/damirm/lazytrade/internal/domain"
	"github.com/damirm/lazytrade/internal/exchange"
	"github.com/damirm/lazytrade/internal/exchange/fake"
	"github.com/damirm/lazytrade/internal/storage"
	"github.com/shopspring/decimal"
)

type reconciliationFixture struct {
	positions []storage.Position
	orders    []storage.LocalOpenOrder
}

type contextRegisteringExchange struct {
	exchange.Exchange
	registered []domain.OrderID
}

func (e *contextRegisteringExchange) RegisterOrderContext(
	orderID domain.OrderID,
	_ domain.StrategyID,
	_ domain.InstrumentID,
	_ domain.OrderSide,
) {
	e.registered = append(e.registered, orderID)
}

func (f reconciliationFixture) ListPositionsByExchange(context.Context, domain.ExchangeAccountID) ([]storage.Position, error) {
	return append([]storage.Position(nil), f.positions...), nil
}

func (f reconciliationFixture) ListOpenOrdersByExchange(context.Context, domain.ExchangeAccountID) ([]storage.LocalOpenOrder, error) {
	return append([]storage.LocalOpenOrder(nil), f.orders...), nil
}

func TestReconcilerHealthySnapshot(t *testing.T) {
	t.Parallel()
	adapter := fake.New("fake", exchange.Capabilities{Sandbox: true})
	adapter.SetPortfolio(exchange.Portfolio{
		AccountID: "fake", AsOf: time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC),
		Positions: []exchange.Position{{
			InstrumentID: "TEST", Quantity: domain.Quantity{Value: decimal.NewFromInt(2)},
		}},
	})
	remoteOrder, err := adapter.PlaceOrder(context.Background(), exchange.NewOrder{
		ClientOrderID: "client-1", StrategyID: "ma", ExchangeAccountID: "fake",
		InstrumentID: "TEST", Side: domain.OrderSideBuy, Type: domain.OrderTypeMarket,
		Quantity: domain.Quantity{Value: decimal.NewFromInt(1)},
	})
	if err != nil {
		t.Fatal(err)
	}
	store := reconciliationFixture{
		positions: []storage.Position{{
			StrategyID: "ma", InstrumentID: "TEST",
			Quantity:     domain.Quantity{Value: decimal.NewFromInt(2)},
			AveragePrice: domain.Price{Value: decimal.NewFromInt(100), Asset: "USD"},
		}},
		orders: []storage.LocalOpenOrder{{
			StrategyID: "ma", InstrumentID: "TEST", ClientOrderID: remoteOrder.ClientOrderID,
			ExchangeOrderID: remoteOrder.ID, Side: domain.OrderSideBuy, Status: "accepted",
			RequestedQuantity: remoteOrder.Quantity, FilledQuantity: remoteOrder.FilledQuantity,
		}},
	}
	report, err := (Reconciler{Exchange: adapter, Store: store}).Reconcile(context.Background(), "fake")
	if err != nil || len(report.Issues) != 0 {
		t.Fatalf("report = %#v, error = %v", report, err)
	}
}

func TestReconcilerRestoresOpenOrderExecutionContext(t *testing.T) {
	adapter := fake.New("fake", exchange.Capabilities{Sandbox: true})
	adapter.SetPortfolio(exchange.Portfolio{AccountID: "fake", AsOf: time.Now().UTC()})
	remote, err := adapter.PlaceOrder(context.Background(), exchange.NewOrder{
		ClientOrderID: "client-1", StrategyID: "ma", ExchangeAccountID: "fake",
		InstrumentID: "TEST", Side: domain.OrderSideSell, Type: domain.OrderTypeMarket,
		Quantity: domain.Quantity{Value: decimal.NewFromInt(1)},
	})
	if err != nil {
		t.Fatal(err)
	}
	wrapped := &contextRegisteringExchange{Exchange: adapter}
	store := reconciliationFixture{orders: []storage.LocalOpenOrder{{
		StrategyID: "ma", InstrumentID: "TEST", ClientOrderID: remote.ClientOrderID,
		ExchangeOrderID: remote.ID, Side: domain.OrderSideSell, Status: "accepted",
		RequestedQuantity: remote.Quantity, FilledQuantity: remote.FilledQuantity,
	}}}
	if _, err := (Reconciler{Exchange: wrapped, Store: store}).Reconcile(context.Background(), "fake"); err != nil {
		t.Fatal(err)
	}
	if len(wrapped.registered) != 1 || wrapped.registered[0] != remote.ID {
		t.Fatalf("registered order contexts = %v", wrapped.registered)
	}
}

func TestReconcilerFailsClosedOnPositionAndOrderMismatch(t *testing.T) {
	t.Parallel()
	adapter := fake.New("fake", exchange.Capabilities{Sandbox: true})
	adapter.SetPortfolio(exchange.Portfolio{
		AccountID: "fake", AsOf: time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC),
		Positions: []exchange.Position{{
			InstrumentID: "TEST", Quantity: domain.Quantity{Value: decimal.NewFromInt(3)},
		}},
	})
	_, err := adapter.PlaceOrder(context.Background(), exchange.NewOrder{
		ClientOrderID: "remote-only", StrategyID: "ma", ExchangeAccountID: "fake",
		InstrumentID: "TEST", Side: domain.OrderSideBuy, Type: domain.OrderTypeMarket,
		Quantity: domain.Quantity{Value: decimal.NewFromInt(1)},
	})
	if err != nil {
		t.Fatal(err)
	}
	store := reconciliationFixture{positions: []storage.Position{{
		StrategyID: "ma", InstrumentID: "TEST",
		Quantity:     domain.Quantity{Value: decimal.NewFromInt(2)},
		AveragePrice: domain.Price{Value: decimal.NewFromInt(100), Asset: "USD"},
	}}}
	report, err := (Reconciler{Exchange: adapter, Store: store}).Reconcile(context.Background(), "fake")
	if !errors.Is(err, ErrReconciliationMismatch) {
		t.Fatalf("error = %v", err)
	}
	if len(report.Issues) != 2 ||
		report.Issues[0].Kind != "open_order_presence" ||
		report.Issues[1].Kind != "position_quantity" {
		t.Fatalf("issues = %#v", report.Issues)
	}
}
