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

	name        string
	instruments []domain.Instrument
	portfolio   exchange.Portfolio
	scenarios   []Scenario
	orders      map[domain.OrderID]domain.Order
	orderSeq    uint64

	marketSubscribers    map[uint64]*marketSubscriber
	executionSubscribers map[uint64]*executionSubscriber
	subscriberSeq        uint64
	connected            bool
}

type marketSubscriber struct {
	subscriptions []exchange.Subscription
	events        chan domain.MarketEvent
	errors        chan error
	done          <-chan struct{}
	cancel        context.CancelFunc
}

type executionSubscriber struct {
	accountID  domain.ExchangeAccountID
	executions chan exchange.Execution
	errors     chan error
	done       <-chan struct{}
	cancel     context.CancelFunc
}

func New(name string) *Exchange {
	return &Exchange{
		name:                 name,
		orders:               make(map[domain.OrderID]domain.Order),
		marketSubscribers:    make(map[uint64]*marketSubscriber),
		executionSubscribers: make(map[uint64]*executionSubscriber),
		connected:            true,
	}
}

func (f *Exchange) Name() string { return f.name }

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
	if err := ctx.Err(); err != nil {
		return exchange.MarketStream{}, err
	}
	for i, subscription := range subscriptions {
		if err := subscription.Validate(); err != nil {
			return exchange.MarketStream{}, fmt.Errorf("subscription %d: %w", i, err)
		}
	}

	f.mu.Lock()
	if !f.connected {
		f.mu.Unlock()
		return exchange.MarketStream{}, exchangeError("subscribe market data", exchange.ErrorTransient)
	}
	f.subscriberSeq++
	id := f.subscriberSeq
	streamCtx, cancel := context.WithCancel(ctx)
	subscriber := &marketSubscriber{
		subscriptions: append([]exchange.Subscription(nil), subscriptions...),
		events:        make(chan domain.MarketEvent, 32),
		errors:        make(chan error, 1),
		done:          streamCtx.Done(),
		cancel:        cancel,
	}
	f.marketSubscribers[id] = subscriber
	f.mu.Unlock()

	go f.removeMarketSubscriber(id, subscriber)
	return exchange.MarketStream{Events: subscriber.events, Errors: subscriber.errors}, nil
}

func (f *Exchange) SubscribeExecutions(ctx context.Context, accountID domain.ExchangeAccountID) (exchange.ExecutionStream, error) {
	if err := ctx.Err(); err != nil {
		return exchange.ExecutionStream{}, err
	}
	if err := accountID.Validate(); err != nil {
		return exchange.ExecutionStream{}, fmt.Errorf("account ID: %w", err)
	}
	f.mu.Lock()
	if !f.connected {
		f.mu.Unlock()
		return exchange.ExecutionStream{}, exchangeError("subscribe executions", exchange.ErrorTransient)
	}
	f.subscriberSeq++
	id := f.subscriberSeq
	streamCtx, cancel := context.WithCancel(ctx)
	subscriber := &executionSubscriber{
		accountID: accountID, executions: make(chan exchange.Execution, 32), errors: make(chan error, 1),
		done: streamCtx.Done(), cancel: cancel,
	}
	f.executionSubscribers[id] = subscriber
	f.mu.Unlock()
	go f.removeExecutionSubscriber(id, subscriber)
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
	f.orders[orderID] = order
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

// Disconnect terminates all current streams. Recovery needs a new subscription
// lifetime; this adapter instance never reconnects.
func (f *Exchange) Disconnect() {
	f.mu.Lock()
	defer f.mu.Unlock()
	if !f.connected {
		return
	}
	f.connected = false
	for _, subscriber := range f.marketSubscribers {
		select {
		case <-subscriber.done:
		default:
			subscriber.errors <- exchangeError("market stream", exchange.ErrorTransient)
		}
		subscriber.cancel()
	}
	for _, subscriber := range f.executionSubscribers {
		select {
		case <-subscriber.done:
		default:
			subscriber.errors <- exchangeError("execution stream", exchange.ErrorTransient)
		}
		subscriber.cancel()
	}
}

func (f *Exchange) removeMarketSubscriber(id uint64, subscriber *marketSubscriber) {
	<-subscriber.done
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.marketSubscribers[id] != subscriber {
		return
	}
	delete(f.marketSubscribers, id)
	close(subscriber.events)
	close(subscriber.errors)
}

func (f *Exchange) removeExecutionSubscriber(id uint64, subscriber *executionSubscriber) {
	<-subscriber.done
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
	message := exchange.Execution{
		ID: execution.ID, OrderID: execution.OrderID,
		ClientOrderID: f.orders[execution.OrderID].ClientOrderID,
		InstrumentID:  execution.InstrumentID, Side: execution.Side,
		Quantity: execution.Quantity, Price: execution.Price, Commission: execution.Commission,
		ExecutedAt: execution.ExecutedAt, ExchangeTrade: execution.ExchangeTrade,
	}
	for _, subscriber := range f.executionSubscribers {
		if subscriber.accountID == accountID {
			subscriber.executions <- message
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
