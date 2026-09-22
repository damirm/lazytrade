package tinvest

import (
	"context"
	"io"
	"testing"
	"time"

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

func TestSubscribeExecutionsMapsTradeWithoutInMemoryOrderContext(t *testing.T) {
	executedAt := time.Date(2026, 7, 29, 10, 30, 0, 0, time.UTC)
	sandbox := &sandboxStub{stateResponse: &pb.OrderState{
		OrderId: "order-1", InstrumentUid: "instrument", LotsExecuted: 2,
		OrderRequestId:     "client-1",
		Direction:          pb.OrderDirection_ORDER_DIRECTION_BUY,
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

	stream, err := adapter.SubscribeExecutions(context.Background(), "sandbox-account")
	if err != nil {
		t.Fatal(err)
	}
	execution, ok := <-stream.Executions
	if !ok {
		t.Fatal("execution stream closed without a trade")
	}
	if execution.ID != "trade-1" || execution.ClientOrderID != "client-1" || execution.InstrumentID != "instrument" ||
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

func TestSubscribeExecutionsFailsClosedForMissingOrderState(t *testing.T) {
	sandbox := &sandboxStub{}
	opener := &tradesOpenerStub{receiver: &tradesReceiverStub{responses: []*pb.TradesStreamResponse{{
		Payload: &pb.TradesStreamResponse_OrderTrades{OrderTrades: &pb.OrderTrades{
			OrderId: "unknown-order", AccountId: "broker-account", Trades: []*pb.OrderTrade{{TradeId: "trade-1"}},
		}},
	}}}}
	adapter := orderTestAdapter(sandbox)
	adapter.orderStream = opener
	stream, err := adapter.SubscribeExecutions(context.Background(), "sandbox-account")
	if err != nil {
		t.Fatal(err)
	}
	if err := <-stream.Errors; err == nil {
		t.Fatal("missing order state did not stop the execution stream")
	}
}

func TestMapOrderTradesValidatesExchangeIdentity(t *testing.T) {
	for _, test := range []struct {
		name      string
		change    func(*pb.OrderTrades, *pb.OrderState)
		wantError bool
	}{
		{"valid without client ID", func(_ *pb.OrderTrades, s *pb.OrderState) { s.OrderRequestId = "" }, false},
		{"foreign account", func(e *pb.OrderTrades, _ *pb.OrderState) { e.AccountId = "other" }, true},
		{"wrong order", func(_ *pb.OrderTrades, s *pb.OrderState) { s.OrderId = "other" }, true},
		{"wrong instrument", func(_ *pb.OrderTrades, s *pb.OrderState) { s.InstrumentUid = "other" }, true},
		{"missing instrument", func(e *pb.OrderTrades, s *pb.OrderState) { e.InstrumentUid = ""; s.InstrumentUid = "" }, true},
		{"unknown direction", func(e *pb.OrderTrades, _ *pb.OrderState) { e.Direction = pb.OrderDirection(999) }, true},
		{"wrong direction", func(_ *pb.OrderTrades, s *pb.OrderState) { s.Direction = pb.OrderDirection_ORDER_DIRECTION_SELL }, true},
		{"missing timestamp", func(e *pb.OrderTrades, _ *pb.OrderState) { e.Trades[0].DateTime = nil }, true},
		{"invalid timestamp", func(e *pb.OrderTrades, _ *pb.OrderState) { e.Trades[0].DateTime.Nanos = -1 }, true},
	} {
		t.Run(test.name, func(t *testing.T) {
			state := &pb.OrderState{
				OrderId: "order-1", OrderRequestId: "client-1", InstrumentUid: "instrument",
				Direction: pb.OrderDirection_ORDER_DIRECTION_BUY, LotsExecuted: 1,
				ExecutedCommission: &pb.MoneyValue{Units: 1, Currency: "rub"},
			}
			trades := &pb.OrderTrades{
				OrderId: "order-1", AccountId: "broker-account", InstrumentUid: "instrument",
				Direction: pb.OrderDirection_ORDER_DIRECTION_BUY,
				Trades: []*pb.OrderTrade{{
					TradeId: "trade-1", Quantity: 10, Price: &pb.Quotation{Units: 100},
					DateTime: timestamppb.New(time.Date(2026, 1, 1, 10, 0, 0, 0, time.UTC)),
				}},
			}
			test.change(trades, state)
			adapter := orderTestAdapter(&sandboxStub{stateResponse: state})
			fills, err := adapter.mapOrderTrades(context.Background(), trades)
			if (err != nil) != test.wantError {
				t.Fatalf("fills = %+v, error = %v", fills, err)
			}
			if err == nil && (len(fills) != 1 || fills[0].ClientOrderID != "") {
				t.Fatalf("fill with optional client ID = %+v", fills)
			}
		})
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
