package tinvest

import (
	"context"
	"testing"
	"time"

	"github.com/damirm/lazytrade/internal/domain"
	"github.com/damirm/lazytrade/internal/exchange"
	"github.com/shopspring/decimal"
	"google.golang.org/grpc"
	"google.golang.org/protobuf/types/known/timestamppb"
	pb "opensource.tbank.ru/invest/invest-go/proto"
)

func TestOrderStateRejectsMalformedFields(t *testing.T) {
	for _, test := range []struct {
		name   string
		change func(*pb.OrderState)
	}{
		{"unspecified direction", func(s *pb.OrderState) { s.Direction = 0 }},
		{"unknown direction", func(s *pb.OrderState) { s.Direction = 999 }},
		{"unspecified type", func(s *pb.OrderState) { s.OrderType = 0 }},
		{"unknown type", func(s *pb.OrderState) { s.OrderType = 999 }},
		{"unspecified status", func(s *pb.OrderState) { s.ExecutionReportStatus = 0 }},
		{"unknown status", func(s *pb.OrderState) { s.ExecutionReportStatus = 999 }},
		{"missing date", func(s *pb.OrderState) { s.OrderDate = nil }},
		{"zero date", func(s *pb.OrderState) { s.OrderDate = timestamppb.New(time.Time{}) }},
		{"invalid date nanos", func(s *pb.OrderState) { s.OrderDate.Nanos = -1 }},
		{"invalid date seconds", func(s *pb.OrderState) { s.OrderDate.Seconds = 253402300800 }},
	} {
		t.Run(test.name, func(t *testing.T) {
			state := historyOrder("order-1", "client-1", pb.OrderDirection_ORDER_DIRECTION_SELL, "trade-1", time.Date(2026, 7, 30, 10, 0, 0, 0, time.UTC))
			state.InitialSecurityPrice = &pb.MoneyValue{Currency: "rub", Units: 100}
			test.change(state)
			adapter := orderTestAdapter(&sandboxStub{stateResponse: state})
			if _, err := adapter.GetOrder(context.Background(), "order-1"); err == nil {
				t.Fatal("malformed order state was accepted")
			}
		})
	}
}

func TestOrderEnumsMapOnlySupportedValues(t *testing.T) {
	for _, side := range []domain.OrderSide{domain.OrderSideBuy, domain.OrderSideSell, 0, 255} {
		mapped, err := mapOrderDirection(side)
		valid := side == domain.OrderSideBuy || side == domain.OrderSideSell
		if (err == nil) != valid {
			t.Fatalf("mapOrderDirection(%q) = %v, %v", side, mapped, err)
		}
		if valid {
			roundtrip, err := orderSide(mapped)
			if err != nil || roundtrip != side {
				t.Fatalf("direction roundtrip = %q, %v, want %q", roundtrip, err, side)
			}
		}
	}
	for _, typ := range []domain.OrderType{domain.OrderTypeMarket, domain.OrderTypeLimit, 0, 255} {
		mapped, err := mapOrderType(typ)
		valid := typ == domain.OrderTypeMarket || typ == domain.OrderTypeLimit
		if (err == nil) != valid {
			t.Fatalf("mapOrderType(%q) = %v, %v", typ, mapped, err)
		}
		if valid {
			roundtrip, err := orderType(mapped)
			if err != nil || roundtrip != typ {
				t.Fatalf("type roundtrip = %q, %v, want %q", roundtrip, err, typ)
			}
		}
	}
	for _, value := range []int32{0, 999, -1} {
		if mapped, err := orderSide(pb.OrderDirection(value)); err == nil || mapped != 0 {
			t.Fatalf("orderSide(%d) = %q, %v", value, mapped, err)
		}
		if mapped, err := orderType(pb.OrderType(value)); err == nil || mapped != 0 {
			t.Fatalf("orderType(%d) = %q, %v", value, mapped, err)
		}
		if mapped, err := mapOrderStatus(pb.OrderExecutionReportStatus(value)); err == nil || mapped != 0 {
			t.Fatalf("mapOrderStatus(%d) = %q, %v", value, mapped, err)
		}
	}
	for _, test := range []struct {
		input pb.OrderExecutionReportStatus
		want  domain.OrderStatus
	}{
		{pb.OrderExecutionReportStatus_EXECUTION_REPORT_STATUS_NEW, domain.OrderStatusAccepted},
		{pb.OrderExecutionReportStatus_EXECUTION_REPORT_STATUS_PARTIALLYFILL, domain.OrderStatusPartiallyFilled},
		{pb.OrderExecutionReportStatus_EXECUTION_REPORT_STATUS_FILL, domain.OrderStatusFilled},
		{pb.OrderExecutionReportStatus_EXECUTION_REPORT_STATUS_CANCELLED, domain.OrderStatusCancelled},
		{pb.OrderExecutionReportStatus_EXECUTION_REPORT_STATUS_REJECTED, domain.OrderStatusRejected},
	} {
		if mapped, err := mapOrderStatus(test.input); err != nil || mapped != test.want {
			t.Fatalf("mapOrderStatus(%s) = %q, %v, want %q", test.input, mapped, err, test.want)
		}
	}
}

func TestPlaceOrderUnknownStatusHasUnknownOutcome(t *testing.T) {
	for _, status := range []pb.OrderExecutionReportStatus{0, 999} {
		t.Run(status.String(), func(t *testing.T) {
			stub := &sandboxStub{postResponse: &pb.PostOrderResponse{
				OrderId: "order-1", OrderRequestId: "client-1", LotsRequested: 1,
				ExecutionReportStatus: status,
			}}
			_, err := orderTestAdapter(stub).PlaceOrder(context.Background(), exchange.NewOrder{
				ClientOrderID: "client-1", StrategyID: "ma", ExchangeAccountID: "sandbox-account", InstrumentID: "instrument",
				Side: domain.OrderSideBuy, Type: domain.OrderTypeMarket, Quantity: domain.Quantity{Value: decimal.NewFromInt(10)},
			})
			assertUnknownMutationOutcome(t, err)
			if stub.postCalls != 1 {
				t.Fatalf("PostOrder calls = %d, want 1", stub.postCalls)
			}
		})
	}
}

type ordersResponseStub struct {
	*sandboxStub
	response *pb.GetOrdersResponse
}

func (s ordersResponseStub) GetOrders(context.Context, *pb.GetOrdersRequest, ...grpc.CallOption) (*pb.GetOrdersResponse, error) {
	return s.response, nil
}

func TestOpenOrdersRejectsMissingResponseAndState(t *testing.T) {
	for _, response := range []*pb.GetOrdersResponse{nil, {Orders: []*pb.OrderState{nil}}} {
		adapter := orderTestAdapter(&sandboxStub{})
		adapter.orders = ordersResponseStub{response: response}
		if _, err := adapter.OpenOrders(context.Background(), "sandbox-account"); err == nil {
			t.Fatal("missing orders response/state was accepted")
		}
	}
}

func TestPlaceOrderRejectsUnknownEnumsBeforeAPI(t *testing.T) {
	for _, change := range []func(*exchange.NewOrder){
		func(order *exchange.NewOrder) { order.Side = 0 },
		func(order *exchange.NewOrder) { order.Side = 255 },
		func(order *exchange.NewOrder) { order.Type = 0 },
		func(order *exchange.NewOrder) { order.Type = 255 },
	} {
		request := exchange.NewOrder{
			ClientOrderID: "client-1", StrategyID: "ma", ExchangeAccountID: "sandbox-account", InstrumentID: "instrument",
			Side: domain.OrderSideBuy, Type: domain.OrderTypeMarket, Quantity: domain.Quantity{Value: decimal.NewFromInt(10)},
		}
		change(&request)
		stub := &sandboxStub{}
		if _, err := orderTestAdapter(stub).PlaceOrder(context.Background(), request); err == nil || stub.postCalls != 0 {
			t.Fatalf("invalid request error = %v, PostOrder calls = %d", err, stub.postCalls)
		}
	}
}

func TestOrderStatePreservesValidMapping(t *testing.T) {
	at := time.Date(2026, 7, 30, 10, 0, 0, 123, time.UTC)
	state := historyOrder("order-1", "client-1", pb.OrderDirection_ORDER_DIRECTION_SELL, "trade-1", at)
	state.OrderType = pb.OrderType_ORDER_TYPE_LIMIT
	state.OrderDate = timestamppb.New(at)
	state.InitialSecurityPrice = &pb.MoneyValue{Currency: "rub", Units: 100, Nano: 500_000_000}
	order, err := orderTestAdapter(&sandboxStub{stateResponse: state}).GetOrder(context.Background(), "order-1")
	if err != nil {
		t.Fatal(err)
	}
	if order.Side != domain.OrderSideSell || order.Type != domain.OrderTypeLimit || order.Status != domain.OrderStatusFilled ||
		!order.SubmittedAt.Equal(at) || !order.UpdatedAt.Equal(at) || order.LimitPrice.Value.String() != "100.5" || order.LimitPrice.Asset != "RUB" {
		t.Fatalf("mapped order = %+v", order)
	}
}

func TestCancelOrderRejectsInvalidResponseTime(t *testing.T) {
	for _, at := range []*timestamppb.Timestamp{nil, {Nanos: -1}, timestamppb.New(time.Time{})} {
		stub := &sandboxStub{cancelResponse: &pb.CancelOrderResponse{Time: at}}
		err := orderTestAdapter(stub).CancelOrder(context.Background(), "order-1")
		assertUnknownMutationOutcome(t, err)
		if stub.cancelCalls != 1 {
			t.Fatalf("CancelOrder calls = %d, want 1", stub.cancelCalls)
		}
	}
}

func TestCancelOrderAcceptsValidResponseTime(t *testing.T) {
	stub := &sandboxStub{cancelResponse: &pb.CancelOrderResponse{Time: timestamppb.New(time.Date(2026, 7, 30, 10, 0, 0, 0, time.UTC))}}
	if err := orderTestAdapter(stub).CancelOrder(context.Background(), "order-1"); err != nil {
		t.Fatal(err)
	}
	if stub.cancelCalls != 1 || stub.cancelRequest.GetOrderId() != "order-1" {
		t.Fatalf("CancelOrder calls = %d, request = %v", stub.cancelCalls, stub.cancelRequest)
	}
}
