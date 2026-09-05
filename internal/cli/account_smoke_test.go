package cli

import (
	"context"
	"errors"
	"fmt"
	"sync"
	"testing"
	"time"

	"github.com/damirm/lazytrade/internal/domain"
	"github.com/damirm/lazytrade/internal/exchange"
	"github.com/shopspring/decimal"
)

type smokeStub struct {
	mu            sync.Mutex
	instrument    domain.Instrument
	position      decimal.Decimal
	orders        map[domain.OrderID]domain.Order
	stream        chan domain.Execution
	streamErrors  chan error
	placeCount    int
	failPlaceCall int
	cancelCount   int
	cancelErr     error
	getOrderCalls int
	getOrderHook  func(int, domain.OrderID) (domain.Order, error)
}

func newSmokeStub(t *testing.T) *smokeStub {
	t.Helper()
	price, err := domain.NewPrice("1", "RUB")
	if err != nil {
		t.Fatal(err)
	}
	return &smokeStub{
		instrument: domain.Instrument{
			ID: "instrument", Symbol: "TEST", BaseAsset: "TEST", QuoteAsset: "RUB", SettlementAsset: "RUB",
			PriceStep: price, QuantityStep: domain.Quantity{Value: decimal.NewFromInt(1)},
		},
		orders: make(map[domain.OrderID]domain.Order), stream: make(chan domain.Execution, 8),
		streamErrors: make(chan error, 1),
	}
}

func (s *smokeStub) Name() string                        { return "sandbox" }
func (s *smokeStub) Capabilities() exchange.Capabilities { return exchange.Capabilities{Sandbox: true} }
func (s *smokeStub) Instruments(context.Context) ([]domain.Instrument, error) {
	return []domain.Instrument{s.instrument}, nil
}
func (s *smokeStub) Portfolio(_ context.Context, accountID domain.ExchangeAccountID) (exchange.Portfolio, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	positions := []exchange.Position(nil)
	if !s.position.IsZero() {
		positions = append(positions, exchange.Position{
			InstrumentID: s.instrument.ID, Quantity: domain.Quantity{Value: s.position},
		})
	}
	return exchange.Portfolio{AccountID: accountID, Positions: positions}, nil
}
func (s *smokeStub) SubscribeMarketData(context.Context, []exchange.Subscription) (exchange.MarketStream, error) {
	return exchange.MarketStream{}, errors.New("not used")
}
func (s *smokeStub) SubscribeExecutions(context.Context, domain.ExchangeAccountID) (exchange.ExecutionStream, error) {
	return exchange.ExecutionStream{Executions: s.stream, Errors: s.streamErrors}, nil
}
func (s *smokeStub) PlaceOrder(_ context.Context, request exchange.NewOrder) (domain.Order, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.placeCount++
	if s.placeCount == s.failPlaceCall {
		return domain.Order{}, errors.New("injected placement failure")
	}
	now := time.Date(2026, 1, 1, 0, 0, s.placeCount, 0, time.UTC)
	id := domain.OrderID(fmt.Sprintf("order-%d", s.placeCount))
	order := domain.Order{
		ID: id, ClientOrderID: request.ClientOrderID, StrategyID: request.StrategyID,
		ExchangeAccountID: request.ExchangeAccountID, InstrumentID: request.InstrumentID,
		Side: request.Side, Type: request.Type, Status: domain.OrderStatusFilled,
		Quantity: request.Quantity, FilledQuantity: request.Quantity, SubmittedAt: now, UpdatedAt: now,
	}
	s.orders[id] = order
	change := request.Quantity.Value
	if request.Side == domain.OrderSideSell {
		change = change.Neg()
	}
	s.position = s.position.Add(change)
	price, _ := domain.NewPrice("100", "RUB")
	commission, _ := domain.NewMoney("1", "RUB")
	s.stream <- domain.Execution{
		ID: domain.ExecutionID(fmt.Sprintf("execution-%d", s.placeCount)), OrderID: id,
		StrategyID: request.StrategyID, InstrumentID: request.InstrumentID, Side: request.Side,
		Quantity: request.Quantity, Price: price, Commission: commission, ExecutedAt: now,
	}
	return order, nil
}
func (s *smokeStub) CancelOrder(_ context.Context, id domain.OrderID) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.cancelCount++
	if s.cancelErr != nil {
		return s.cancelErr
	}
	order := s.orders[id]
	order.Status = domain.OrderStatusCancelled
	s.orders[id] = order
	return nil
}
func (s *smokeStub) GetOrder(_ context.Context, id domain.OrderID) (domain.Order, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.getOrderCalls++
	if s.getOrderHook != nil {
		return s.getOrderHook(s.getOrderCalls, id)
	}
	order, ok := s.orders[id]
	if !ok {
		return domain.Order{}, errors.New("not found")
	}
	return order, nil
}

func unknownCancelError() error {
	return &exchange.Error{
		Operation: "cancel order", Category: exchange.ErrorUnknownOutcome,
		Outcome: exchange.OutcomeUnknown, Message: "ambiguous cancellation",
	}
}

func smokeOrder(status domain.OrderStatus) domain.Order {
	now := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	return domain.Order{
		ID: "order-1", ClientOrderID: "123e4567-e89b-52d3-a456-426614174000",
		StrategyID: smokeStrategyID, ExchangeAccountID: "sandbox", InstrumentID: "instrument",
		Side: domain.OrderSideBuy, Type: domain.OrderTypeMarket, Status: status,
		Quantity:       domain.Quantity{Value: decimal.NewFromInt(1)},
		FilledQuantity: domain.Quantity{Value: decimal.Zero}, SubmittedAt: now, UpdatedAt: now,
	}
}

func TestCancelOnceAndObserveResolvesUnknownOutcomeFromTerminalOrder(t *testing.T) {
	for _, status := range []domain.OrderStatus{domain.OrderStatusCancelled, domain.OrderStatusFilled, domain.OrderStatusRejected} {
		status := status
		t.Run(fmt.Sprint(status), func(t *testing.T) {
			stub := newSmokeStub(t)
			stub.cancelErr = unknownCancelError()
			stub.orders["order-1"] = smokeOrder(status)
			order, err := cancelOnceAndObserve(context.Background(), stub, "order-1")
			if err != nil || order.Status != status {
				t.Fatalf("order=%#v error=%v", order, err)
			}
			if stub.cancelCount != 1 {
				t.Fatalf("CancelOrder calls = %d, want 1", stub.cancelCount)
			}
		})
	}
}

func TestCancelOnceAndObserveDoesNotRepeatUnknownActiveCancellation(t *testing.T) {
	stub := newSmokeStub(t)
	stub.cancelErr = unknownCancelError()
	stub.orders["order-1"] = smokeOrder(domain.OrderStatusAccepted)
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Millisecond)
	defer cancel()
	_, err := cancelOnceAndObserve(ctx, stub, "order-1")
	if !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("error = %v, want deadline", err)
	}
	if stub.cancelCount != 1 {
		t.Fatalf("CancelOrder calls = %d, want 1", stub.cancelCount)
	}
}

func TestCancelOnceAndObserveToleratesTemporaryNotFound(t *testing.T) {
	stub := newSmokeStub(t)
	stub.cancelErr = unknownCancelError()
	stub.getOrderHook = func(call int, _ domain.OrderID) (domain.Order, error) {
		if call == 1 {
			return domain.Order{}, &exchange.Error{
				Operation: "get order", Category: exchange.ErrorNotFound,
				Outcome: exchange.OutcomeKnownNotApplied, Message: "not visible",
			}
		}
		return smokeOrder(domain.OrderStatusCancelled), nil
	}
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	order, err := cancelOnceAndObserve(ctx, stub, "order-1")
	if err != nil || order.Status != domain.OrderStatusCancelled {
		t.Fatalf("order=%#v error=%v", order, err)
	}
	if stub.cancelCount != 1 || stub.getOrderCalls != 2 {
		t.Fatalf("cancel calls=%d get calls=%d", stub.cancelCount, stub.getOrderCalls)
	}
}
func (s *smokeStub) GetOrderByClientID(context.Context, domain.ClientOrderID) (domain.Order, error) {
	return domain.Order{}, errors.New("not used")
}
func (s *smokeStub) OpenOrders(context.Context, domain.ExchangeAccountID) ([]domain.Order, error) {
	return nil, nil
}

func TestRunSandboxSmokeTestCompletesRoundTrip(t *testing.T) {
	stub := newSmokeStub(t)
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	report, err := runSandboxSmokeTest(ctx, stub, "sandbox", "instrument", domain.Quantity{Value: decimal.NewFromInt(1)})
	if err != nil {
		t.Fatal(err)
	}
	if report.BuyExecutions != 1 || report.SellExecutions != 1 || !report.FinalQuantity.IsZero() {
		t.Fatalf("report = %#v", report)
	}
	if stub.placeCount != 2 || !stub.position.IsZero() {
		t.Fatalf("orders = %d, position = %s", stub.placeCount, stub.position)
	}
}

func TestRunSandboxSmokeTestAttemptsCleanupWhenSellFails(t *testing.T) {
	stub := newSmokeStub(t)
	stub.failPlaceCall = 2
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	report, err := runSandboxSmokeTest(ctx, stub, "sandbox", "instrument", domain.Quantity{Value: decimal.NewFromInt(1)})
	if err == nil {
		t.Fatal("expected failure")
	}
	if !report.CleanupAttempted || report.CleanupError != nil {
		t.Fatalf("report = %#v", report)
	}
	if stub.placeCount != 3 || !stub.position.IsZero() {
		t.Fatalf("cleanup orders = %d, position = %s", stub.placeCount, stub.position)
	}
}

func TestRunSandboxSmokeTestRefusesExistingPosition(t *testing.T) {
	stub := newSmokeStub(t)
	stub.position = decimal.NewFromInt(1)
	_, err := runSandboxSmokeTest(context.Background(), stub, "sandbox", "instrument",
		domain.Quantity{Value: decimal.NewFromInt(1)})
	if err == nil {
		t.Fatal("existing position was accepted")
	}
	if stub.placeCount != 0 {
		t.Fatalf("placed %d orders", stub.placeCount)
	}
}
