package tinvest

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/damirm/lazytrade/internal/domain"
	"github.com/damirm/lazytrade/internal/exchange"
	"google.golang.org/protobuf/types/known/timestamppb"
	pb "opensource.tbank.ru/invest/invest-go/proto"
)

func TestSandboxExecutionHistoryPaginatesAndBridgesOrderStages(t *testing.T) {
	from := time.Date(2026, 7, 30, 10, 0, 0, 0, time.UTC)
	to := from.Add(time.Hour)
	stub := &sandboxStub{
		historyResponses: []*pb.GetOperationsByCursorResponse{
			{HasNext: true, NextCursor: "page-2", Items: []*pb.OperationItem{historyOperation("operation-1", pb.OperationType_OPERATION_TYPE_BUY)}},
			{Items: []*pb.OperationItem{historyOperation("operation-2", pb.OperationType_OPERATION_TYPE_SELL)}},
		},
		stateResponses: map[string]*pb.OrderState{
			"operation-1": historyOrder("exchange-1", "123e4567-e89b-52d3-a456-426614174001", pb.OrderDirection_ORDER_DIRECTION_BUY, "trade-1", from.Add(time.Minute)),
			"operation-2": historyOrder("exchange-2", "123e4567-e89b-52d3-a456-426614174002", pb.OrderDirection_ORDER_DIRECTION_SELL, "trade-2", from.Add(2*time.Minute)),
		},
	}
	adapter := orderTestAdapter(stub)
	history, err := adapter.ExecutionHistory(context.Background(), exchange.ExecutionHistoryRequest{
		AccountID: "sandbox-account", From: from, To: to,
	})
	if err != nil {
		t.Fatal(err)
	}
	if !history.Complete || len(history.Orders) != 2 || len(stub.historyRequests) != 2 {
		t.Fatalf("history = %#v, requests = %d", history, len(stub.historyRequests))
	}
	if stub.historyRequests[0].GetAccountId() != "broker-account" ||
		stub.historyRequests[0].GetLimit() != 1000 || stub.historyRequests[0].GetWithoutTrades() ||
		stub.historyRequests[0].GetWithoutCommissions() || stub.historyRequests[1].GetCursor() != "page-2" {
		t.Fatalf("requests = %#v", stub.historyRequests)
	}
	order := history.Orders[0]
	if order.ExchangeOrderID != "exchange-1" || order.ClientOrderID != "123e4567-e89b-52d3-a456-426614174001" ||
		order.Side != domain.OrderSideBuy || !order.Complete || len(order.Fills) != 1 {
		t.Fatalf("order = %#v", order)
	}
	if fill := order.Fills[0]; fill.TradeID != "trade-1" || fill.Quantity.Value.String() != "20" ||
		fill.Price.Value.String() != "100.5" || fill.Price.Asset != "RUB" {
		t.Fatalf("fill = %#v", fill)
	}
	if order.CumulativeCommission.Amount.String() != "1.5" || order.CumulativeCommission.Asset != "RUB" {
		t.Fatalf("commission = %#v", order.CumulativeCommission)
	}
}

func TestSandboxExecutionHistoryFailsWhenPaginationDoesNotAdvance(t *testing.T) {
	from := time.Date(2026, 7, 30, 10, 0, 0, 0, time.UTC)
	stub := &sandboxStub{historyResponses: []*pb.GetOperationsByCursorResponse{
		{HasNext: true, NextCursor: ""},
	}}
	_, err := orderTestAdapter(stub).ExecutionHistory(context.Background(), exchange.ExecutionHistoryRequest{
		AccountID: "sandbox-account", From: from, To: from.Add(time.Hour),
	})
	if err == nil || !strings.Contains(err.Error(), "did not advance") {
		t.Fatalf("error = %v", err)
	}
}

func TestSandboxExecutionHistoryFailsClosedOnOrderMismatch(t *testing.T) {
	from := time.Date(2026, 7, 30, 10, 0, 0, 0, time.UTC)
	operation := historyOperation("operation-1", pb.OperationType_OPERATION_TYPE_BUY)
	operation.InstrumentUid = "another-instrument"
	stub := &sandboxStub{
		historyResponses: []*pb.GetOperationsByCursorResponse{{Items: []*pb.OperationItem{operation}}},
		stateResponses: map[string]*pb.OrderState{
			"operation-1": historyOrder("exchange-1", "123e4567-e89b-52d3-a456-426614174001", pb.OrderDirection_ORDER_DIRECTION_BUY, "trade-1", from),
		},
	}
	_, err := orderTestAdapter(stub).ExecutionHistory(context.Background(), exchange.ExecutionHistoryRequest{
		AccountID: "sandbox-account", From: from, To: from.Add(time.Hour),
	})
	if err == nil || !strings.Contains(err.Error(), "changed instrument") {
		t.Fatalf("error = %v", err)
	}
}

func historyOperation(id string, operationType pb.OperationType) *pb.OperationItem {
	return &pb.OperationItem{Id: id, BrokerAccountId: "broker-account", InstrumentUid: "instrument", Type: operationType, State: pb.OperationState_OPERATION_STATE_EXECUTED}
}

func historyOrder(id, clientID string, direction pb.OrderDirection, tradeID string, at time.Time) *pb.OrderState {
	return &pb.OrderState{
		OrderId: id, OrderRequestId: clientID, InstrumentUid: "instrument", Direction: direction,
		OrderType: pb.OrderType_ORDER_TYPE_MARKET, OrderDate: timestamppb.New(at.Add(-time.Minute)),
		ExecutionReportStatus: pb.OrderExecutionReportStatus_EXECUTION_REPORT_STATUS_FILL,
		LotsRequested:         2, LotsExecuted: 2,
		ExecutedCommission: &pb.MoneyValue{Currency: "RUB", Units: 1, Nano: 500_000_000},
		Stages:             []*pb.OrderStage{{TradeId: tradeID, Quantity: 2, Price: &pb.MoneyValue{Currency: "RUB", Units: 100, Nano: 500_000_000}, ExecutionTime: timestamppb.New(at)}},
	}
}
