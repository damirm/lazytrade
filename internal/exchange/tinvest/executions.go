package tinvest

import (
	"context"
	"errors"
	"fmt"
	"io"

	"github.com/damirm/lazytrade/internal/domain"
	"github.com/damirm/lazytrade/internal/exchange"
	"github.com/shopspring/decimal"
	pb "opensource.tbank.ru/invest/invest-go/proto"
)

type executionOrderContext struct {
	StrategyID   domain.StrategyID
	InstrumentID domain.InstrumentID
	Side         domain.OrderSide
}

type tradesReceiver interface {
	Recv() (*pb.TradesStreamResponse, error)
}

type tradesOpener interface {
	OpenTrades(context.Context, *pb.TradesStreamRequest) (tradesReceiver, error)
}

type grpcTradesOpener struct{ client pb.OrdersStreamServiceClient }

func (o grpcTradesOpener) OpenTrades(ctx context.Context, request *pb.TradesStreamRequest) (tradesReceiver, error) {
	return o.client.TradesStream(ctx, request)
}

func (a *Adapter) RegisterOrderContext(
	orderID domain.OrderID,
	strategyID domain.StrategyID,
	instrumentID domain.InstrumentID,
	side domain.OrderSide,
) {
	if orderID.Validate() != nil || strategyID.Validate() != nil ||
		instrumentID.Validate() != nil || (side != domain.OrderSideBuy && side != domain.OrderSideSell) {
		return
	}
	a.orderContextMu.Lock()
	defer a.orderContextMu.Unlock()
	if a.orderContexts == nil {
		a.orderContexts = make(map[domain.OrderID]executionOrderContext)
	}
	a.orderContexts[orderID] = executionOrderContext{
		StrategyID: strategyID, InstrumentID: instrumentID, Side: side,
	}
}

func (a *Adapter) registerClientOrderContext(clientID domain.ClientOrderID, orderContext executionOrderContext) {
	if clientID.Validate() != nil {
		return
	}
	a.orderContextMu.Lock()
	defer a.orderContextMu.Unlock()
	if a.clientContexts == nil {
		a.clientContexts = make(map[domain.ClientOrderID]executionOrderContext)
	}
	a.clientContexts[clientID] = orderContext
}

func (a *Adapter) SubscribeExecutions(ctx context.Context, accountID domain.ExchangeAccountID) (exchange.ExecutionStream, error) {
	if err := a.validateAccount(accountID); err != nil {
		return exchange.ExecutionStream{}, err
	}
	if a.orderStream == nil {
		return exchange.ExecutionStream{}, errors.New("T-Invest order stream is not configured")
	}
	receiver, err := a.orderStream.OpenTrades(ctx, &pb.TradesStreamRequest{Accounts: []string{a.accountID}})
	if err != nil {
		return exchange.ExecutionStream{}, mapError("subscribe executions", err)
	}
	executions := make(chan domain.Execution, 32)
	streamErrors := make(chan error, 1)
	go a.receiveExecutions(ctx, receiver, executions, streamErrors)
	return exchange.ExecutionStream{Executions: executions, Errors: streamErrors}, nil
}

func (a *Adapter) receiveExecutions(
	ctx context.Context,
	receiver tradesReceiver,
	executions chan<- domain.Execution,
	streamErrors chan<- error,
) {
	defer close(executions)
	defer close(streamErrors)
	for {
		response, err := receiver.Recv()
		if err != nil {
			if errors.Is(err, io.EOF) || ctx.Err() != nil {
				return
			}
			select {
			case streamErrors <- mapError("receive execution", err):
			case <-ctx.Done():
			}
			return
		}
		if subscription := response.GetSubscription(); subscription != nil {
			if err := validateTradesSubscription(subscription, a.accountID); err != nil {
				select {
				case streamErrors <- err:
				case <-ctx.Done():
				}
				return
			}
			continue
		}
		trades := response.GetOrderTrades()
		if trades == nil {
			continue
		}
		mapped, err := a.mapOrderTrades(ctx, trades)
		if err != nil {
			select {
			case streamErrors <- err:
			case <-ctx.Done():
			}
			return
		}
		for _, execution := range mapped {
			select {
			case executions <- execution:
			case <-ctx.Done():
				return
			}
		}
	}
}

func validateTradesSubscription(subscription *pb.SubscriptionResponse, accountID string) error {
	if subscription.GetStatus() != pb.ResultSubscriptionStatus_RESULT_SUBSCRIPTION_STATUS_OK {
		detail := subscription.GetError()
		if detail == nil {
			return fmt.Errorf("execution subscription status %s", subscription.GetStatus())
		}
		return fmt.Errorf("execution subscription rejected: code=%q message=%q", detail.GetCode(), detail.GetMessage())
	}
	if subscription.GetStreamId() == "" {
		return errors.New("execution subscription confirmation has no stream ID")
	}
	accounts := subscription.GetAccounts()
	if len(accounts) != 1 || accounts[0] != accountID {
		return fmt.Errorf("execution subscription confirmed unexpected accounts %v", accounts)
	}
	return nil
}

func (a *Adapter) mapOrderTrades(ctx context.Context, trades *pb.OrderTrades) ([]domain.Execution, error) {
	orderID := domain.OrderID(trades.GetOrderId())
	state, err := a.getRawOrderState(ctx, string(orderID), pb.OrderIdType_ORDER_ID_TYPE_EXCHANGE)
	if err != nil {
		return nil, err
	}
	a.orderContextMu.RLock()
	orderContext, registeredByOrder := a.orderContexts[orderID]
	ok := registeredByOrder
	if !ok {
		orderContext, ok = a.clientContexts[domain.ClientOrderID(state.GetOrderRequestId())]
	}
	a.orderContextMu.RUnlock()
	if !ok {
		return nil, fmt.Errorf("execution for unregistered order %q", orderID)
	}
	if trades.GetAccountId() != a.accountID {
		return nil, fmt.Errorf("execution order %q belongs to unexpected account %q", orderID, trades.GetAccountId())
	}
	if domain.InstrumentID(trades.GetInstrumentUid()) != orderContext.InstrumentID {
		return nil, fmt.Errorf("execution order %q belongs to unexpected instrument %q", orderID, trades.GetInstrumentUid())
	}
	if orderSide(trades.GetDirection()) != orderContext.Side {
		return nil, fmt.Errorf("execution order %q has unexpected direction", orderID)
	}
	if !registeredByOrder {
		a.RegisterOrderContext(orderID, orderContext.StrategyID, orderContext.InstrumentID, orderContext.Side)
	}
	instrument, err := a.Instrument(ctx, orderContext.InstrumentID)
	if err != nil {
		return nil, fmt.Errorf("execution instrument: %w", err)
	}
	commission, err := money(state.GetExecutedCommission())
	if err != nil {
		return nil, fmt.Errorf("execution commission: %w", err)
	}
	totalExecuted := lotsToQuantity(state.GetLotsExecuted(), instrument.QuantityStep).Value
	if !totalExecuted.IsPositive() {
		return nil, errors.New("execution order state has no executed quantity")
	}
	result := make([]domain.Execution, 0, len(trades.GetTrades()))
	for _, trade := range trades.GetTrades() {
		if trade.GetTradeId() == "" || trade.GetQuantity() <= 0 {
			return nil, errors.New("execution trade ID and positive quantity are required")
		}
		quantity := decimal.NewFromInt(trade.GetQuantity())
		tradePrice, mapErr := price(trade.GetPrice(), instrument.QuoteAsset)
		if mapErr != nil {
			return nil, fmt.Errorf("execution price: %w", mapErr)
		}
		executedAt := trades.GetCreatedAt()
		if trade.GetDateTime() != nil {
			executedAt = trade.GetDateTime()
		}
		if executedAt == nil {
			return nil, errors.New("execution time is missing")
		}
		execution := domain.Execution{
			ID: domain.ExecutionID(trade.GetTradeId()), OrderID: orderID,
			StrategyID: orderContext.StrategyID, InstrumentID: orderContext.InstrumentID,
			Side: orderContext.Side, Quantity: domain.Quantity{Value: quantity}, Price: tradePrice,
			Commission: domain.Money{
				Amount: commission.Amount.Mul(quantity).Div(totalExecuted),
				Asset:  commission.Asset,
			},
			ExecutedAt: executedAt.AsTime().UTC(), ExchangeTrade: trade.GetTradeId(),
		}
		if err := execution.Validate(); err != nil {
			return nil, fmt.Errorf("map execution %q: %w", trade.GetTradeId(), err)
		}
		result = append(result, execution)
	}
	return result, nil
}

func (a *Adapter) getRawOrderState(ctx context.Context, id string, idType pb.OrderIdType) (*pb.OrderState, error) {
	request := &pb.GetOrderStateRequest{
		AccountId: a.accountID, OrderId: id, OrderIdType: &idType,
	}
	policy := a.readRetryPolicy()
	if policy.MaxAttempts > 2 {
		policy.MaxAttempts = 2
	}
	state, err := retryRead(ctx, "get execution order", a.timeout, policy, func(callCtx context.Context) (*pb.OrderState, error) {
		return a.orders.GetOrderState(callCtx, request)
	})
	if err != nil {
		return nil, err
	}
	return state, nil
}

var _ exchange.OrderContextRegistrar = (*Adapter)(nil)
