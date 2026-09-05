package fake

import (
	"context"
	"testing"
	"time"

	"github.com/damirm/lazytrade/internal/domain"
	"github.com/damirm/lazytrade/internal/exchange"
)

func TestOrderScenariosAndErrorCategories(t *testing.T) {
	tests := []struct {
		name     string
		scenario OrderScenario
		category exchange.ErrorCategory
		outcome  exchange.Outcome
	}{
		{"reject", OrderReject, exchange.ErrorRejected, exchange.OutcomeKnownNotApplied},
		{"transient", OrderTransient, exchange.ErrorTransient, exchange.OutcomeKnownNotApplied},
		{"rate limit", OrderRateLimited, exchange.ErrorRateLimited, exchange.OutcomeKnownNotApplied},
		{"unknown outcome", OrderUnknownOutcome, exchange.ErrorUnknownOutcome, exchange.OutcomeUnknown},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			fake := New("fake", exchange.Capabilities{Sandbox: true})
			fake.Enqueue(Scenario{Kind: tt.scenario})
			_, err := fake.PlaceOrder(context.Background(), orderRequest(t))
			if !exchange.IsCategory(err, tt.category) {
				t.Fatalf("error = %v, want category %v", err, tt.category)
			}
			exchangeErr := err.(*exchange.Error)
			if exchangeErr.Outcome != tt.outcome {
				t.Fatalf("outcome = %v, want %v", exchangeErr.Outcome, tt.outcome)
			}
			if tt.scenario == OrderUnknownOutcome {
				order, lookupErr := fake.GetOrderByClientID(context.Background(), "client")
				if lookupErr != nil || order.ClientOrderID != "client" {
					t.Fatalf("unknown outcome cannot be reconciled: order = %+v, err = %v", order, lookupErr)
				}
			}
		})
	}
}

func TestPartialAndMultipleFills(t *testing.T) {
	tests := []struct {
		name       string
		scenario   Scenario
		wantStatus domain.OrderStatus
		wantIDs    []domain.ExecutionID
	}{
		{
			name: "partial", scenario: Scenario{Kind: OrderPartialFill, Fills: []domain.Execution{execution(t, "fill-1", "0.4")}},
			wantStatus: domain.OrderStatusPartiallyFilled, wantIDs: []domain.ExecutionID{"fill-1"},
		},
		{
			name: "multiple", scenario: Scenario{Kind: OrderMultipleFills, Fills: []domain.Execution{
				execution(t, "fill-1", "0.4"), execution(t, "fill-2", "0.6"),
			}},
			wantStatus: domain.OrderStatusFilled, wantIDs: []domain.ExecutionID{"fill-1", "fill-2"},
		},
		{
			name: "duplicate", scenario: Scenario{Kind: OrderDuplicateFill, Fills: []domain.Execution{execution(t, "fill-1", "1")}},
			wantStatus: domain.OrderStatusFilled, wantIDs: []domain.ExecutionID{"fill-1", "fill-1"},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			fake := New("fake", exchange.Capabilities{MarketOrders: true, Sandbox: true})
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			stream, err := fake.SubscribeExecutions(ctx, "account")
			if err != nil {
				t.Fatal(err)
			}
			fake.Enqueue(tt.scenario)
			order, err := fake.PlaceOrder(context.Background(), orderRequest(t))
			if err != nil {
				t.Fatal(err)
			}
			if order.Status != tt.wantStatus {
				t.Fatalf("status = %v, want %v", order.Status, tt.wantStatus)
			}
			for _, id := range tt.wantIDs {
				select {
				case fill := <-stream.Executions:
					if fill.ID != id {
						t.Fatalf("fill ID = %q, want %q", fill.ID, id)
					}
				default:
					t.Fatalf("fill %q was not published synchronously", id)
				}
			}
		})
	}
}

func TestMarketDisconnectReconnectRestoresSubscriptions(t *testing.T) {
	fake := New("fake", exchange.Capabilities{StreamingCandles: true})
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	subscription := exchange.Subscription{
		InstrumentID: "instrument", Kind: exchange.SubscriptionCandles, Interval: time.Minute,
	}
	stream, err := fake.SubscribeMarketData(ctx, []exchange.Subscription{subscription})
	if err != nil {
		t.Fatal(err)
	}
	initial := receiveState(t, stream.State)
	if initial.State != exchange.StreamHealthy || initial.Generation != 1 {
		t.Fatalf("initial state = %+v", initial)
	}

	fake.Disconnect()
	if err := receiveError(t, stream.Errors); !exchange.IsCategory(err, exchange.ErrorTransient) {
		t.Fatalf("disconnect error = %v", err)
	}
	disconnected := receiveState(t, stream.State)
	if disconnected.State != exchange.StreamDisconnected {
		t.Fatalf("state = %v, want disconnected", disconnected.State)
	}

	fake.Reconnect()
	reconnected := receiveState(t, stream.State)
	if reconnected.State != exchange.StreamReconnected || reconnected.Generation != 2 {
		t.Fatalf("reconnected state = %+v", reconnected)
	}
	if len(reconnected.Subscriptions) != 1 || reconnected.Subscriptions[0] != subscription {
		t.Fatalf("subscriptions not restored: %+v", reconnected.Subscriptions)
	}

	event := candleEvent(t)
	if err := fake.PublishMarket(event); err != nil {
		t.Fatal(err)
	}
	select {
	case got := <-stream.Events:
		if got.Sequence != event.Sequence {
			t.Fatalf("sequence = %d, want %d", got.Sequence, event.Sequence)
		}
	default:
		t.Fatal("market event was not published synchronously")
	}
}

func TestCapabilitiesAndOrderLifecycle(t *testing.T) {
	capabilities := exchange.Capabilities{MarketOrders: true, LimitOrders: true, Sandbox: true}
	fake := New("fake", capabilities)
	if fake.Name() != "fake" || fake.Capabilities() != capabilities {
		t.Fatal("identity or capabilities mismatch")
	}
	fake.Enqueue(Scenario{Kind: OrderSuccess})
	order, err := fake.PlaceOrder(context.Background(), orderRequest(t))
	if err != nil {
		t.Fatal(err)
	}
	open, err := fake.OpenOrders(context.Background(), "account")
	if err != nil || len(open) != 1 {
		t.Fatalf("open orders = %v, err = %v", open, err)
	}
	if err := fake.CancelOrder(context.Background(), order.ID); err != nil {
		t.Fatal(err)
	}
	got, err := fake.GetOrder(context.Background(), order.ID)
	if err != nil || got.Status != domain.OrderStatusCancelled {
		t.Fatalf("cancelled order = %+v, err = %v", got, err)
	}
}

func TestMarketQueueOverflowIsReportedWithoutBlocking(t *testing.T) {
	fake := New("fake", exchange.Capabilities{StreamingCandles: true})
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	_, err := fake.SubscribeMarketData(ctx, []exchange.Subscription{{
		InstrumentID: "instrument", Kind: exchange.SubscriptionCandles, Interval: time.Minute,
	}})
	if err != nil {
		t.Fatal(err)
	}
	event := candleEvent(t)
	for i := 0; i < 32; i++ {
		event.Sequence = uint64(i + 1)
		if err := fake.PublishMarket(event); err != nil {
			t.Fatalf("publish %d: %v", i, err)
		}
	}
	event.Sequence = 33
	err = fake.PublishMarket(event)
	if !exchange.IsCategory(err, exchange.ErrorTransient) {
		t.Fatalf("overflow error = %v, want transient classified error", err)
	}
}

func orderRequest(t *testing.T) exchange.NewOrder {
	t.Helper()
	quantity, err := domain.NewQuantity("1")
	if err != nil {
		t.Fatal(err)
	}
	return exchange.NewOrder{
		ClientOrderID: "client", StrategyID: "strategy", ExchangeAccountID: "account",
		InstrumentID: "instrument", Side: domain.OrderSideBuy, Type: domain.OrderTypeMarket,
		Quantity: quantity,
	}
}

func execution(t *testing.T, id, quantity string) domain.Execution {
	t.Helper()
	q, err := domain.NewQuantity(quantity)
	if err != nil {
		t.Fatal(err)
	}
	price, _ := domain.NewPrice("100", "RUB")
	commission, _ := domain.NewMoney("0.05", "RUB")
	return domain.Execution{
		ID: domain.ExecutionID(id), OrderID: "fake-order-1", StrategyID: "strategy", InstrumentID: "instrument",
		Side: domain.OrderSideBuy, Quantity: q, Price: price, Commission: commission,
		ExecutedAt: time.Date(2026, 1, 1, 0, 0, 1, 0, time.UTC),
	}
}

func candleEvent(t *testing.T) domain.MarketEvent {
	t.Helper()
	price := func(value string) domain.Price {
		result, err := domain.NewPrice(value, "RUB")
		if err != nil {
			t.Fatal(err)
		}
		return result
	}
	quantity, _ := domain.NewQuantity("10")
	start := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	return domain.MarketEvent{
		ExchangeAccountID: "account", InstrumentID: "instrument",
		Kind: domain.MarketEventCandleClose, ExchangeTime: start.Add(time.Minute),
		ReceivedTime: start.Add(time.Minute), Sequence: 1,
		Candle: &domain.Candle{
			Start: start, End: start.Add(time.Minute), Interval: time.Minute,
			Open: price("100"), High: price("101"), Low: price("99"), Close: price("100"),
			Volume: quantity, Complete: true,
		},
	}
}

func receiveState(t *testing.T, channel <-chan exchange.StreamEvent) exchange.StreamEvent {
	t.Helper()
	select {
	case event := <-channel:
		return event
	default:
		t.Fatal("stream state was not published synchronously")
		return exchange.StreamEvent{}
	}
}

func receiveError(t *testing.T, channel <-chan error) error {
	t.Helper()
	select {
	case err := <-channel:
		return err
	default:
		t.Fatal("stream error was not published synchronously")
		return nil
	}
}
