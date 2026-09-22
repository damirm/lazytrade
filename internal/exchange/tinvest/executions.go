package tinvest

import (
	"context"
	"errors"
	"fmt"

	"github.com/damirm/lazytrade/internal/domain"
	"github.com/damirm/lazytrade/internal/exchange"
	"github.com/shopspring/decimal"
	pb "opensource.tbank.ru/invest/invest-go/proto"
)

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

func (a *Adapter) SubscribeExecutions(ctx context.Context, accountID domain.ExchangeAccountID) (exchange.ExecutionStream, error) {
	if err := a.validateAccount(accountID); err != nil {
		return exchange.ExecutionStream{}, err
	}
	if a.orderStream == nil {
		return exchange.ExecutionStream{}, errors.New("T-Invest order stream is not configured")
	}
	streamCtx, cancel := context.WithCancel(ctx)
	receiver, err := a.orderStream.OpenTrades(streamCtx, &pb.TradesStreamRequest{Accounts: []string{a.accountID}})
	if err != nil {
		cancel()
		return exchange.ExecutionStream{}, mapError("subscribe executions", err)
	}
	executions := make(chan exchange.Execution, 32)
	streamErrors := make(chan error, 1)
	go a.receiveExecutions(ctx, streamCtx, cancel, receiver, executions, streamErrors)
	return exchange.ExecutionStream{Executions: executions, Errors: streamErrors}, nil
}

func (a *Adapter) receiveExecutions(
	parentCtx, streamCtx context.Context,
	cancel context.CancelFunc,
	receiver tradesReceiver,
	executions chan<- exchange.Execution,
	streamErrors chan<- error,
) {
	defer close(executions)
	defer close(streamErrors)
	defer cancel()
	for {
		response, err := receiver.Recv()
		if err != nil {
			if parentCtx.Err() == nil {
				streamErrors <- mapError("receive execution", err)
			}
			return
		}
		if subscription := response.GetSubscription(); subscription != nil {
			if err := validateTradesSubscription(subscription, a.accountID); err != nil {
				if parentCtx.Err() == nil {
					streamErrors <- err
				}
				return
			}
			continue
		}
		trades := response.GetOrderTrades()
		if trades == nil {
			continue
		}
		mapped, err := a.mapOrderTrades(streamCtx, trades)
		if err != nil {
			if parentCtx.Err() == nil {
				streamErrors <- err
			}
			return
		}
		for _, execution := range mapped {
			select {
			case executions <- execution:
			case <-streamCtx.Done():
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

func (a *Adapter) mapOrderTrades(ctx context.Context, trades *pb.OrderTrades) ([]exchange.Execution, error) {
	orderID := domain.OrderID(trades.GetOrderId())
	if err := orderID.Validate(); err != nil {
		return nil, fmt.Errorf("execution order ID: %w", err)
	}
	if trades.GetAccountId() != a.accountID {
		return nil, fmt.Errorf("execution order %q belongs to an unexpected account", orderID)
	}
	state, err := a.getRawOrderState(ctx, string(orderID), pb.OrderIdType_ORDER_ID_TYPE_EXCHANGE)
	if err != nil {
		return nil, err
	}
	if state == nil || state.GetOrderId() != string(orderID) {
		return nil, errors.New("execution order state has an unexpected order ID")
	}
	instrumentID := domain.InstrumentID(trades.GetInstrumentUid())
	if err := instrumentID.Validate(); err != nil || state.GetInstrumentUid() != string(instrumentID) {
		return nil, fmt.Errorf("execution order %q has an inconsistent instrument", orderID)
	}
	side, err := orderSide(trades.GetDirection())
	if err != nil {
		return nil, fmt.Errorf("execution direction: %w", err)
	}
	if state.GetDirection() != trades.GetDirection() {
		return nil, errors.New("execution direction disagrees with order state")
	}
	instrument, err := a.Instrument(ctx, instrumentID)
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
	result := make([]exchange.Execution, 0, len(trades.GetTrades()))
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
		timestamp, err := requiredTime(executedAt)
		if err != nil {
			return nil, fmt.Errorf("execution time: %w", err)
		}
		execution := exchange.Execution{
			ID: domain.ExecutionID(trade.GetTradeId()), OrderID: orderID,
			ClientOrderID: domain.ClientOrderID(state.GetOrderRequestId()), InstrumentID: instrumentID,
			Side: side, Quantity: domain.Quantity{Value: quantity}, Price: tradePrice,
			Commission: domain.Money{
				Amount: commission.Amount.Mul(quantity).Div(totalExecuted),
				Asset:  commission.Asset,
			},
			ExecutedAt: timestamp, ExchangeTrade: trade.GetTradeId(),
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
