package fake

import (
	"context"
	"fmt"
	"sync"
	"time"

	"github.com/damirm/lazytrade/internal/domain"
	"github.com/damirm/lazytrade/internal/exchange"
)

type OrderScenario uint8

const (
	OrderSuccess OrderScenario = iota + 1
	OrderReject
	OrderPartialFill
	OrderMultipleFills
	OrderDuplicateFill
	OrderTransient
	OrderRateLimited
	OrderUnknownOutcome
)

type Scenario struct {
	Kind  OrderScenario
	Fills []domain.Execution
}

// Exchange is a deterministic in-memory adapter intended for engine and
// adapter contract tests. Every externally visible transition is test-driven.
type Exchange struct {
	mu sync.Mutex

	name         string
	capabilities exchange.Capabilities
	instruments  []domain.Instrument
	portfolio    exchange.Portfolio
	scenarios    []Scenario
	orders       map[domain.OrderID]domain.Order
	orderSeq     uint64

	marketSubscribers    map[uint64]*marketSubscriber
	executionSubscribers map[uint64]*executionSubscriber
	subscriberSeq        uint64
	generation           uint64
	connected            bool
}

type marketSubscriber struct {
	subscriptions []exchange.Subscription
	events        chan domain.MarketEvent
	errors        chan error
	state         chan exchange.StreamEvent
}

type executionSubscriber struct {
	accountID  domain.ExchangeAccountID
	executions chan domain.Execution
	errors     chan error
}

func New(name string, capabilities exchange.Capabilities) *Exchange {
	return &Exchange{
		name:                 name,
		capabilities:         capabilities,
		orders:               make(map[domain.OrderID]domain.Order),
		marketSubscribers:    make(map[uint64]*marketSubscriber),
		executionSubscribers: make(map[uint64]*executionSubscriber),
		generation:           1,
		connected:            true,
	}
}

func (f *Exchange) Name() string                        { return f.name }
func (f *Exchange) Capabilities() exchange.Capabilities { return f.capabilities }

func (f *Exchange) SetInstruments(instruments []domain.Instrument) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.instruments = append([]domain.Instrument(nil), instruments...)
}

func (f *Exchange) SetPortfolio(portfolio exchange.Portfolio) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.portfolio = portfolio
}

func (f *Exchange) Enqueue(scenarios ...Scenario) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.scenarios = append(f.scenarios, scenarios...)
}

func (f *Exchange) Instruments(context.Context) ([]domain.Instrument, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]domain.Instrument(nil), f.instruments...), nil
}

func (f *Exchange) Portfolio(_ context.Context, accountID domain.ExchangeAccountID) (exchange.Portfolio, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.portfolio.AccountID != accountID {
		return exchange.Portfolio{}, exchangeError("portfolio", exchange.ErrorNotFound)
	}
	return f.portfolio, nil
}

func (f *Exchange) SubscribeMarketData(ctx context.Context, subscriptions []exchange.Subscription) (exchange.MarketStream, error) {
	for i, subscription := range subscriptions {
		if err := subscription.Validate(); err != nil {
			return exchange.MarketStream{}, fmt.Errorf("subscription %d: %w", i, err)
		}
	}

	f.mu.Lock()
	f.subscriberSeq++
	id := f.subscriberSeq
	subscriber := &marketSubscriber{
		subscriptions: append([]exchange.Subscription(nil), subscriptions...),
		events:        make(chan domain.MarketEvent, 32),
		errors:        make(chan error, 8),
		state:         make(chan exchange.StreamEvent, 8),
	}
	f.marketSubscribers[id] = subscriber
	generation := f.generation
	state := exchange.StreamHealthy
	if !f.connected {
		state = exchange.StreamDisconnected
	}
	f.mu.Unlock()

	subscriber.state <- exchange.StreamEvent{
		State: state, Generation: generation,
		Subscriptions: append([]exchange.Subscription(nil), subscriptions...),
	}
	go f.removeMarketSubscriber(ctx, id, subscriber)
	return exchange.MarketStream{Events: subscriber.events, Errors: subscriber.errors, State: subscriber.state}, nil
}

func (f *Exchange) SubscribeExecutions(ctx context.Context, accountID domain.ExchangeAccountID) (exchange.ExecutionStream, error) {
	if err := accountID.Validate(); err != nil {
		return exchange.ExecutionStream{}, fmt.Errorf("account ID: %w", err)
	}
	f.mu.Lock()
	f.subscriberSeq++
	id := f.subscriberSeq
	subscriber := &executionSubscriber{
		accountID: accountID, executions: make(chan domain.Execution, 32), errors: make(chan error, 8),
	}
	f.executionSubscribers[id] = subscriber
	f.mu.Unlock()
	go f.removeExecutionSubscriber(ctx, id, subscriber)
	return exchange.ExecutionStream{Executions: subscriber.executions, Errors: subscriber.errors}, nil
}

func (f *Exchange) PlaceOrder(_ context.Context, request exchange.NewOrder) (domain.Order, error) {
	if err := request.Validate(); err != nil {
		return domain.Order{}, &exchange.Error{
			Operation: "place order", Category: exchange.ErrorInvalidRequest,
			Outcome: exchange.OutcomeKnownNotApplied, Message: err.Error(),
		}
	}

	f.mu.Lock()
	defer f.mu.Unlock()
	if !f.connected {
		return domain.Order{}, exchangeError("place order", exchange.ErrorTransient)
	}
	scenario := Scenario{Kind: OrderSuccess}
	if len(f.scenarios) > 0 {
		scenario = f.scenarios[0]
		f.scenarios = f.scenarios[1:]
	}
	switch scenario.Kind {
	case OrderReject:
		return domain.Order{}, exchangeError("place order", exchange.ErrorRejected)
	case OrderTransient:
		return domain.Order{}, exchangeError("place order", exchange.ErrorTransient)
	case OrderRateLimited:
		return domain.Order{}, exchangeError("place order", exchange.ErrorRateLimited)
	}

	f.orderSeq++
	orderID := domain.OrderID(fmt.Sprintf("fake-order-%d", f.orderSeq))
	now := time.Date(2026, 1, 1, 0, 0, int(f.orderSeq), 0, time.UTC)
	order := domain.Order{
		ID: orderID, ClientOrderID: request.ClientOrderID, StrategyID: request.StrategyID,
		ExchangeAccountID: request.ExchangeAccountID, InstrumentID: request.InstrumentID,
		Side: request.Side, Type: request.Type, Status: domain.OrderStatusAccepted,
		Quantity: request.Quantity, LimitPrice: request.LimitPrice, SubmittedAt: now, UpdatedAt: now,
	}
	fills := append([]domain.Execution(nil), scenario.Fills...)
	switch scenario.Kind {
	case OrderPartialFill:
		order.Status = domain.OrderStatusPartiallyFilled
	case OrderMultipleFills, OrderDuplicateFill:
		order.Status = domain.OrderStatusFilled
	case OrderSuccess, OrderUnknownOutcome:
	default:
		return domain.Order{}, exchangeError("place order", exchange.ErrorPermanent)
	}
	for _, fill := range fills {
		order.FilledQuantity.Value = order.FilledQuantity.Value.Add(fill.Quantity.Value)
		f.publishExecutionLocked(request.ExchangeAccountID, fill)
	}
	if scenario.Kind == OrderDuplicateFill && len(fills) > 0 {
		f.publishExecutionLocked(request.ExchangeAccountID, fills[len(fills)-1])
	}
	f.orders[orderID] = order
	if scenario.Kind == OrderUnknownOutcome {
		return domain.Order{}, exchangeError("place order", exchange.ErrorUnknownOutcome)
	}
	return order, nil
}

func (f *Exchange) CancelOrder(_ context.Context, orderID domain.OrderID) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	order, ok := f.orders[orderID]
	if !ok {
		return exchangeError("cancel order", exchange.ErrorNotFound)
	}
	order.Status = domain.OrderStatusCancelled
	f.orders[orderID] = order
	return nil
}

func (f *Exchange) GetOrder(_ context.Context, orderID domain.OrderID) (domain.Order, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	order, ok := f.orders[orderID]
	if !ok {
		return domain.Order{}, exchangeError("get order", exchange.ErrorNotFound)
	}
	return order, nil
}

func (f *Exchange) GetOrderByClientID(_ context.Context, clientOrderID domain.ClientOrderID) (domain.Order, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	for _, order := range f.orders {
		if order.ClientOrderID == clientOrderID {
			return order, nil
		}
	}
	return domain.Order{}, exchangeError("get order by client ID", exchange.ErrorNotFound)
}

func (f *Exchange) OpenOrders(_ context.Context, accountID domain.ExchangeAccountID) ([]domain.Order, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	var orders []domain.Order
	for _, order := range f.orders {
		if order.ExchangeAccountID == accountID &&
			(order.Status == domain.OrderStatusAccepted || order.Status == domain.OrderStatusPartiallyFilled) {
			orders = append(orders, order)
		}
	}
	return orders, nil
}

// PublishMarket injects an event into all matching subscriptions.
func (f *Exchange) PublishMarket(event domain.MarketEvent) error {
	if err := event.Validate(); err != nil {
		return err
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	if !f.connected {
		return exchangeError("publish market", exchange.ErrorTransient)
	}
	for _, subscriber := range f.marketSubscribers {
		if matches(subscriber.subscriptions, event) {
			select {
			case subscriber.events <- event:
			default:
				return &exchange.Error{
					Operation: "publish market", Category: exchange.ErrorTransient,
					Outcome: exchange.OutcomeKnownNotApplied, Retryable: true,
					Message: "market subscriber queue is full",
				}
			}
		}
	}
	return nil
}

// Disconnect and Reconnect deterministically emulate stream lifecycle without
// timers. Desired subscriptions remain attached to each subscriber.
func (f *Exchange) Disconnect() {
	f.mu.Lock()
	defer f.mu.Unlock()
	if !f.connected {
		return
	}
	f.connected = false
	err := exchangeError("market stream", exchange.ErrorTransient)
	for _, subscriber := range f.marketSubscribers {
		subscriber.errors <- err
		subscriber.state <- exchange.StreamEvent{
			State: exchange.StreamDisconnected, Generation: f.generation,
			Subscriptions: append([]exchange.Subscription(nil), subscriber.subscriptions...),
		}
	}
}

func (f *Exchange) Reconnect() {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.connected {
		return
	}
	f.connected = true
	f.generation++
	for _, subscriber := range f.marketSubscribers {
		subscriber.state <- exchange.StreamEvent{
			State: exchange.StreamReconnected, Generation: f.generation,
			Subscriptions: append([]exchange.Subscription(nil), subscriber.subscriptions...),
		}
	}
}

func (f *Exchange) removeMarketSubscriber(ctx context.Context, id uint64, subscriber *marketSubscriber) {
	<-ctx.Done()
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.marketSubscribers[id] != subscriber {
		return
	}
	delete(f.marketSubscribers, id)
	close(subscriber.events)
	close(subscriber.errors)
	subscriber.state <- exchange.StreamEvent{State: exchange.StreamClosed, Generation: f.generation}
	close(subscriber.state)
}

func (f *Exchange) removeExecutionSubscriber(ctx context.Context, id uint64, subscriber *executionSubscriber) {
	<-ctx.Done()
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.executionSubscribers[id] != subscriber {
		return
	}
	delete(f.executionSubscribers, id)
	close(subscriber.executions)
	close(subscriber.errors)
}

func (f *Exchange) publishExecutionLocked(accountID domain.ExchangeAccountID, execution domain.Execution) {
	for _, subscriber := range f.executionSubscribers {
		if subscriber.accountID == accountID {
			subscriber.executions <- execution
		}
	}
}

func matches(subscriptions []exchange.Subscription, event domain.MarketEvent) bool {
	for _, subscription := range subscriptions {
		if subscription.InstrumentID != event.InstrumentID {
			continue
		}
		if (subscription.Kind == exchange.SubscriptionCandles &&
			(event.Kind == domain.MarketEventCandleOpen || event.Kind == domain.MarketEventCandleClose)) ||
			(subscription.Kind == exchange.SubscriptionTrades && event.Kind == domain.MarketEventTrade) ||
			(subscription.Kind == exchange.SubscriptionOrderBook && event.Kind == domain.MarketEventOrderBook) {
			return true
		}
	}
	return false
}

func exchangeError(operation string, category exchange.ErrorCategory) *exchange.Error {
	err := &exchange.Error{Operation: operation, Category: category, Outcome: exchange.OutcomeKnownNotApplied}
	switch category {
	case exchange.ErrorRateLimited:
		err.Message, err.Retryable = "rate limited", true
	case exchange.ErrorTransient:
		err.Message, err.Retryable = "temporary exchange failure", true
	case exchange.ErrorUnknownOutcome:
		err.Message, err.Outcome = "order outcome is unknown", exchange.OutcomeUnknown
	case exchange.ErrorRejected:
		err.Message = "order rejected"
	case exchange.ErrorNotFound:
		err.Message = "resource not found"
	default:
		err.Message = "exchange operation failed"
	}
	return err
}
