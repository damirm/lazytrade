package tinvest

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/damirm/lazytrade/internal/domain"
	"github.com/damirm/lazytrade/internal/exchange"
	"github.com/shopspring/decimal"
	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
	"google.golang.org/protobuf/types/known/timestamppb"
	pb "opensource.tbank.ru/invest/invest-go/proto"
)

type sandboxStub struct {
	openRequest      *pb.OpenSandboxAccountRequest
	openResponse     *pb.OpenSandboxAccountResponse
	openErr          error
	openCalls        int
	payInRequest     *pb.SandboxPayInRequest
	payInResponse    *pb.SandboxPayInResponse
	payInErr         error
	payInCalls       int
	cancelRequest    *pb.CancelOrderRequest
	cancelResponse   *pb.CancelOrderResponse
	cancelErr        error
	cancelCalls      int
	postRequest      *pb.PostOrderRequest
	stateRequest     *pb.GetOrderStateRequest
	postResponse     *pb.PostOrderResponse
	stateResponse    *pb.OrderState
	accountsResponse *pb.GetAccountsResponse
	postErr          error
	postCalls        int
	historyRequests  []*pb.GetOperationsByCursorRequest
	historyResponses []*pb.GetOperationsByCursorResponse
	historyErrors    []error
	stateResponses   map[string]*pb.OrderState
}

func (s *sandboxStub) OpenSandboxAccount(_ context.Context, request *pb.OpenSandboxAccountRequest, _ ...grpc.CallOption) (*pb.OpenSandboxAccountResponse, error) {
	s.openCalls++
	s.openRequest = request
	return s.openResponse, s.openErr
}
func (s *sandboxStub) GetSandboxAccounts(context.Context, *pb.GetAccountsRequest, ...grpc.CallOption) (*pb.GetAccountsResponse, error) {
	if s.accountsResponse == nil {
		return &pb.GetAccountsResponse{}, nil
	}
	return s.accountsResponse, nil
}
func (s *sandboxStub) SandboxPayIn(_ context.Context, request *pb.SandboxPayInRequest, _ ...grpc.CallOption) (*pb.SandboxPayInResponse, error) {
	s.payInCalls++
	s.payInRequest = request
	return s.payInResponse, s.payInErr
}

func (s *sandboxStub) GetSandboxOperationsByCursor(_ context.Context, request *pb.GetOperationsByCursorRequest, _ ...grpc.CallOption) (*pb.GetOperationsByCursorResponse, error) {
	s.historyRequests = append(s.historyRequests, request)
	if len(s.historyErrors) > 0 {
		err := s.historyErrors[0]
		s.historyErrors = s.historyErrors[1:]
		if err != nil {
			return nil, err
		}
	}
	if len(s.historyResponses) > 0 {
		response := s.historyResponses[0]
		s.historyResponses = s.historyResponses[1:]
		return response, nil
	}
	return &pb.GetOperationsByCursorResponse{}, nil
}
func (s *sandboxStub) GetSandboxOrderState(_ context.Context, request *pb.GetOrderStateRequest, _ ...grpc.CallOption) (*pb.OrderState, error) {
	s.stateRequest = request
	if s.stateResponses != nil {
		return s.stateResponses[request.GetOrderId()], nil
	}
	return s.stateResponse, nil
}
func (s *sandboxStub) PostOrder(_ context.Context, request *pb.PostOrderRequest, _ ...grpc.CallOption) (*pb.PostOrderResponse, error) {
	s.postCalls++
	s.postRequest = request
	return s.postResponse, s.postErr
}

func TestPlaceOrderTransportFailureHasUnknownOutcome(t *testing.T) {
	stub := &sandboxStub{postErr: status.Error(codes.DeadlineExceeded, "deadline")}
	adapter := orderTestAdapter(stub)
	_, err := adapter.PlaceOrder(context.Background(), exchange.NewOrder{
		ClientOrderID: "123e4567-e89b-52d3-a456-426614174000", StrategyID: "ma",
		ExchangeAccountID: "sandbox-account", InstrumentID: "instrument",
		Side: domain.OrderSideBuy, Type: domain.OrderTypeMarket,
		Quantity: domain.Quantity{Value: decimal.NewFromInt(10)},
	})
	var exchangeErr *exchange.Error
	if !errors.As(err, &exchangeErr) {
		t.Fatalf("error type = %T", err)
	}
	if exchangeErr.Outcome != exchange.OutcomeUnknown ||
		exchangeErr.Category != exchange.ErrorUnknownOutcome || exchangeErr.Retryable {
		t.Fatalf("mapped error = %+v", exchangeErr)
	}
	if exchangeErr.Operation != "place order" {
		t.Fatalf("operation = %q", exchangeErr.Operation)
	}
}

func TestPlaceOrderCancellationHasUnknownOutcome(t *testing.T) {
	stub := &sandboxStub{postErr: context.Canceled}
	adapter := orderTestAdapter(stub)
	_, err := adapter.PlaceOrder(context.Background(), exchange.NewOrder{
		ClientOrderID: "123e4567-e89b-52d3-a456-426614174000", StrategyID: "ma",
		ExchangeAccountID: "sandbox-account", InstrumentID: "instrument",
		Side: domain.OrderSideBuy, Type: domain.OrderTypeMarket,
		Quantity: domain.Quantity{Value: decimal.NewFromInt(10)},
	})
	var exchangeErr *exchange.Error
	if !errors.As(err, &exchangeErr) {
		t.Fatalf("error type = %T", err)
	}
	if exchangeErr.Outcome != exchange.OutcomeUnknown ||
		exchangeErr.Category != exchange.ErrorUnknownOutcome || exchangeErr.Retryable {
		t.Fatalf("mapped error = %+v", exchangeErr)
	}
}

func TestPlaceOrderMalformedResponseHasUnknownOutcome(t *testing.T) {
	stub := &sandboxStub{}
	adapter := orderTestAdapter(stub)
	_, err := adapter.PlaceOrder(context.Background(), exchange.NewOrder{
		ClientOrderID: "123e4567-e89b-52d3-a456-426614174000", StrategyID: "ma",
		ExchangeAccountID: "sandbox-account", InstrumentID: "instrument",
		Side: domain.OrderSideBuy, Type: domain.OrderTypeMarket,
		Quantity: domain.Quantity{Value: decimal.NewFromInt(10)},
	})
	assertUnknownMutationOutcome(t, err)
	if stub.postCalls != 1 {
		t.Fatalf("PostOrder calls = %d, want 1", stub.postCalls)
	}
}

func (s *sandboxStub) CancelOrder(_ context.Context, request *pb.CancelOrderRequest, _ ...grpc.CallOption) (*pb.CancelOrderResponse, error) {
	s.cancelCalls++
	s.cancelRequest = request
	return s.cancelResponse, s.cancelErr
}

func TestCancelOrderMalformedResponseHasUnknownOutcome(t *testing.T) {
	stub := &sandboxStub{cancelResponse: &pb.CancelOrderResponse{}}
	adapter := orderTestAdapter(stub)
	err := adapter.CancelOrder(context.Background(), "exchange-order")
	assertUnknownMutationOutcome(t, err)
	if stub.cancelCalls != 1 {
		t.Fatalf("CancelOrder calls = %d, want 1", stub.cancelCalls)
	}
}
func (s *sandboxStub) GetOrderState(_ context.Context, request *pb.GetOrderStateRequest, _ ...grpc.CallOption) (*pb.OrderState, error) {
	s.stateRequest = request
	if response := s.stateResponses[request.GetOrderId()]; response != nil {
		return response, nil
	}
	return s.stateResponse, nil
}
func (*sandboxStub) GetOrders(context.Context, *pb.GetOrdersRequest, ...grpc.CallOption) (*pb.GetOrdersResponse, error) {
	return &pb.GetOrdersResponse{}, nil
}
func (*sandboxStub) GetPortfolio(context.Context, *pb.PortfolioRequest, ...grpc.CallOption) (*pb.PortfolioResponse, error) {
	return &pb.PortfolioResponse{}, nil
}

func TestPlaceOrderMapsUnitsToLotsAndIdempotencyKey(t *testing.T) {
	stub := &sandboxStub{postResponse: &pb.PostOrderResponse{
		OrderId: "exchange-order", OrderRequestId: "123e4567-e89b-52d3-a456-426614174000",
		ExecutionReportStatus: pb.OrderExecutionReportStatus_EXECUTION_REPORT_STATUS_NEW,
		LotsRequested:         2,
	}}
	adapter := orderTestAdapter(stub)
	limit := domain.Price{Value: decimal.RequireFromString("123.456789001"), Asset: "RUB"}
	order, err := adapter.PlaceOrder(context.Background(), exchange.NewOrder{
		ClientOrderID: "123e4567-e89b-52d3-a456-426614174000", StrategyID: "ma",
		ExchangeAccountID: "sandbox-account", InstrumentID: "instrument",
		Side: domain.OrderSideBuy, Type: domain.OrderTypeLimit,
		Quantity: domain.Quantity{Value: decimal.NewFromInt(20)}, LimitPrice: &limit,
	})
	if err != nil {
		t.Fatal(err)
	}
	if stub.postRequest.GetQuantity() != 2 {
		t.Fatalf("API quantity = %d lots, want 2", stub.postRequest.GetQuantity())
	}
	if stub.postRequest.GetAccountId() != "broker-account" {
		t.Fatalf("API account ID = %q", stub.postRequest.GetAccountId())
	}
	if stub.postRequest.GetOrderId() != "123e4567-e89b-52d3-a456-426614174000" {
		t.Fatalf("idempotency key = %q", stub.postRequest.GetOrderId())
	}
	if got := stub.postRequest.GetPrice(); got.GetUnits() != 123 || got.GetNano() != 456789001 {
		t.Fatalf("API price = %d/%d", got.GetUnits(), got.GetNano())
	}
	if !order.Quantity.Value.Equal(decimal.NewFromInt(20)) || order.Status != domain.OrderStatusAccepted {
		t.Fatalf("mapped order = %+v", order)
	}
}

func TestPlaceOrderRejectsFractionalLotsBeforeAPI(t *testing.T) {
	stub := &sandboxStub{}
	adapter := orderTestAdapter(stub)
	_, err := adapter.PlaceOrder(context.Background(), exchange.NewOrder{
		ClientOrderID: "123e4567-e89b-52d3-a456-426614174000", StrategyID: "ma",
		ExchangeAccountID: "sandbox-account", InstrumentID: "instrument",
		Side: domain.OrderSideBuy, Type: domain.OrderTypeMarket,
		Quantity: domain.Quantity{Value: decimal.NewFromInt(15)},
	})
	if err == nil {
		t.Fatal("fractional lot was accepted")
	}
	if stub.postRequest != nil {
		t.Fatal("API was called for invalid quantity")
	}
}

func TestGetOrderByClientIDUsesRequestIDType(t *testing.T) {
	stub := &sandboxStub{stateResponse: &pb.OrderState{
		OrderId: "exchange-order", OrderRequestId: "123e4567-e89b-52d3-a456-426614174000",
		ExecutionReportStatus: pb.OrderExecutionReportStatus_EXECUTION_REPORT_STATUS_PARTIALLYFILL,
		LotsRequested:         2, LotsExecuted: 1, InstrumentUid: "instrument",
		Direction: pb.OrderDirection_ORDER_DIRECTION_BUY, OrderType: pb.OrderType_ORDER_TYPE_MARKET,
		OrderDate: timestamppb.New(time.Date(2026, 7, 29, 9, 0, 0, 0, time.UTC)),
	}}
	adapter := orderTestAdapter(stub)
	order, err := adapter.GetOrderByClientID(context.Background(), "123e4567-e89b-52d3-a456-426614174000")
	if err != nil {
		t.Fatal(err)
	}
	if stub.stateRequest.GetOrderIdType() != pb.OrderIdType_ORDER_ID_TYPE_REQUEST {
		t.Fatalf("order ID type = %v", stub.stateRequest.GetOrderIdType())
	}
	if !order.Quantity.Value.Equal(decimal.NewFromInt(20)) ||
		!order.FilledQuantity.Value.Equal(decimal.NewFromInt(10)) {
		t.Fatalf("mapped quantities = %s/%s", order.Quantity.Value, order.FilledQuantity.Value)
	}
}

func orderTestAdapter(service *sandboxStub) *Adapter {
	instrument := domain.Instrument{
		ID: "instrument", ExchangeAccount: "tinvest", Symbol: "TEST", Name: "Test",
		BaseAsset: "TEST", QuoteAsset: "RUB", SettlementAsset: "RUB",
		PriceStep:    domain.Price{Value: decimal.New(1, -2), Asset: "RUB"},
		QuantityStep: domain.Quantity{Value: decimal.NewFromInt(10)},
		MinQuantity:  domain.Quantity{Value: decimal.NewFromInt(10)},
	}
	return &Adapter{
		name: "sandbox-account", accountID: "broker-account", timeout: time.Second,
		sandbox: service, orders: service, operations: service,
		metadata: map[domain.InstrumentID]domain.Instrument{"instrument": instrument},
	}
}
