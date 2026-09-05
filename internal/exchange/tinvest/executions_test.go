package tinvest

import (
	"context"
	"io"
	"testing"
	"time"

	"github.com/damirm/lazytrade/internal/domain"
	"github.com/shopspring/decimal"
	"google.golang.org/protobuf/types/known/timestamppb"
	pb "opensource.tbank.ru/invest/invest-go/proto"
)

type tradesReceiverStub struct {
	responses []*pb.TradesStreamResponse
	index     int
}

func (r *tradesReceiverStub) Recv() (*pb.TradesStreamResponse, error) {
	if r.index >= len(r.responses) {
		return nil, io.EOF
	}
	response := r.responses[r.index]
	r.index++
	return response, nil
}

type tradesOpenerStub struct {
	request  *pb.TradesStreamRequest
	receiver tradesReceiver
}

func (o *tradesOpenerStub) OpenTrades(_ context.Context, request *pb.TradesStreamRequest) (tradesReceiver, error) {
	o.request = request
	return o.receiver, nil
}

func TestSubscribeExecutionsMapsTradeAndActualCommission(t *testing.T) {
	executedAt := time.Date(2026, 7, 29, 10, 30, 0, 0, time.UTC)
	sandbox := &sandboxStub{stateResponse: &pb.OrderState{
		OrderId: "order-1", InstrumentUid: "instrument", LotsExecuted: 2,
		OrderRequestId:     "client-1",
		ExecutedCommission: &pb.MoneyValue{Units: 2, Currency: "rub"},
	}}
	opener := &tradesOpenerStub{receiver: &tradesReceiverStub{responses: []*pb.TradesStreamResponse{{
		Payload: &pb.TradesStreamResponse_OrderTrades{OrderTrades: &pb.OrderTrades{
			OrderId: "order-1", AccountId: "broker-account", InstrumentUid: "instrument",
			Direction: pb.OrderDirection_ORDER_DIRECTION_BUY,
			Trades: []*pb.OrderTrade{{
				TradeId: "trade-1", Quantity: 10, Price: &pb.Quotation{Units: 100, Nano: 50},
				DateTime: timestamppb.New(executedAt),
			}},
		}},
	}}}}
	adapter := orderTestAdapter(sandbox)
	adapter.orderStream = opener
	adapter.registerClientOrderContext("client-1", executionOrderContext{
		StrategyID: "ma", InstrumentID: "instrument", Side: domain.OrderSideBuy,
	})

	stream, err := adapter.SubscribeExecutions(context.Background(), "sandbox-account")
	if err != nil {
		t.Fatal(err)
	}
	execution, ok := <-stream.Executions
	if !ok {
		t.Fatal("execution stream closed without a trade")
	}
	if execution.ID != "trade-1" || execution.StrategyID != "ma" ||
		!execution.Quantity.Value.Equal(decimal.NewFromInt(10)) ||
		!execution.Commission.Amount.Equal(decimal.NewFromInt(1)) {
		t.Fatalf("mapped execution = %+v", execution)
	}
	if execution.Price.Value.String() != "100.00000005" || execution.Price.Asset != "RUB" {
		t.Fatalf("mapped price = %+v", execution.Price)
	}
	if !execution.ExecutedAt.Equal(executedAt) {
		t.Fatalf("executed at = %s", execution.ExecutedAt)
	}
	if len(opener.request.GetAccounts()) != 1 || opener.request.GetAccounts()[0] != "broker-account" {
		t.Fatalf("stream accounts = %v", opener.request.GetAccounts())
	}
	if err := <-stream.Errors; err != nil {
		t.Fatalf("stream error = %v", err)
	}
}

func TestSubscribeExecutionsFailsClosedForUnknownOrder(t *testing.T) {
	sandbox := &sandboxStub{}
	opener := &tradesOpenerStub{receiver: &tradesReceiverStub{responses: []*pb.TradesStreamResponse{{
		Payload: &pb.TradesStreamResponse_OrderTrades{OrderTrades: &pb.OrderTrades{
			OrderId: "unknown-order", Trades: []*pb.OrderTrade{{TradeId: "trade-1"}},
		}},
	}}}}
	adapter := orderTestAdapter(sandbox)
	adapter.orderStream = opener
	stream, err := adapter.SubscribeExecutions(context.Background(), "sandbox-account")
	if err != nil {
		t.Fatal(err)
	}
	if err := <-stream.Errors; err == nil {
		t.Fatal("unknown order did not stop the execution stream")
	}
}

func TestSubscribeExecutionsAcceptsValidSubscriptionConfirmation(t *testing.T) {
	opener := &tradesOpenerStub{receiver: &tradesReceiverStub{responses: []*pb.TradesStreamResponse{{
		Payload: &pb.TradesStreamResponse_Subscription{Subscription: &pb.SubscriptionResponse{
			Status:   pb.ResultSubscriptionStatus_RESULT_SUBSCRIPTION_STATUS_OK,
			StreamId: "stream-1",
			Accounts: []string{"broker-account"},
		}},
	}}}}
	adapter := orderTestAdapter(&sandboxStub{})
	adapter.orderStream = opener

	stream, err := adapter.SubscribeExecutions(context.Background(), "sandbox-account")
	if err != nil {
		t.Fatal(err)
	}
	if _, ok := <-stream.Executions; ok {
		t.Fatal("execution stream did not close after EOF")
	}
	if err := <-stream.Errors; err != nil {
		t.Fatalf("stream error = %v", err)
	}
}

func TestSubscribeExecutionsRejectsInvalidSubscriptionConfirmation(t *testing.T) {
	opener := &tradesOpenerStub{receiver: &tradesReceiverStub{responses: []*pb.TradesStreamResponse{{
		Payload: &pb.TradesStreamResponse_Subscription{Subscription: &pb.SubscriptionResponse{
			Status:   pb.ResultSubscriptionStatus_RESULT_SUBSCRIPTION_STATUS_OK,
			StreamId: "stream-1",
			Accounts: []string{"another-account"},
		}},
	}}}}
	adapter := orderTestAdapter(&sandboxStub{})
	adapter.orderStream = opener

	stream, err := adapter.SubscribeExecutions(context.Background(), "sandbox-account")
	if err != nil {
		t.Fatal(err)
	}
	if err := <-stream.Errors; err == nil {
		t.Fatal("unexpected account confirmation did not stop the execution stream")
	}
}
