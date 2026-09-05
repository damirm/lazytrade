package tinvest

import (
	"context"
	"errors"
	"fmt"
	"sort"
	"time"

	"github.com/damirm/lazytrade/internal/domain"
	"github.com/damirm/lazytrade/internal/exchange"
	"google.golang.org/protobuf/types/known/timestamppb"
	pb "opensource.tbank.ru/invest/invest-go/proto"
)

const maxExecutionHistoryPages = 10_000

const ExecutionHistorySource = "tinvest_sandbox_operations_v1"

// ExecutionHistory implements sandbox history recovery only. Production is
// intentionally not enabled while the adapter itself is sandbox-only.
func (a *Adapter) ExecutionHistory(ctx context.Context, request exchange.ExecutionHistoryRequest) (exchange.ExecutionHistory, error) {
	if err := a.validateAccount(request.AccountID); err != nil {
		return exchange.ExecutionHistory{}, err
	}
	if request.From.IsZero() || request.To.IsZero() || request.From.Location() != time.UTC || request.To.Location() != time.UTC || !request.From.Before(request.To) {
		return exchange.ExecutionHistory{}, errors.New("execution history requires a non-empty increasing UTC window")
	}
	if a.sandbox == nil || a.orders == nil {
		return exchange.ExecutionHistory{}, errors.New("T-Invest sandbox history services are not configured")
	}

	result := exchange.ExecutionHistory{From: request.From, To: request.To}
	byOrder := make(map[domain.OrderID]exchange.RecoveredOrderSnapshot)
	cursor := ""
	seenCursors := make(map[string]struct{})
	for page := 0; page < maxExecutionHistoryPages; page++ {
		limit := int32(1000)
		apiRequest := &pb.GetOperationsByCursorRequest{
			AccountId: a.accountID, From: timestamppb.New(request.From), To: timestamppb.New(request.To),
			Limit: &limit,
		}
		if cursor != "" {
			apiRequest.Cursor = &cursor
		}
		response, err := retryRead(ctx, "get sandbox execution history", a.timeout, a.readRetryPolicy(), func(callCtx context.Context) (*pb.GetOperationsByCursorResponse, error) {
			return a.sandbox.GetSandboxOperationsByCursor(callCtx, apiRequest)
		})
		if err != nil {
			return exchange.ExecutionHistory{}, err
		}
		if response == nil {
			return exchange.ExecutionHistory{}, errors.New("sandbox execution history response is missing")
		}
		for _, item := range response.GetItems() {
			if item.GetType() != pb.OperationType_OPERATION_TYPE_BUY &&
				item.GetType() != pb.OperationType_OPERATION_TYPE_SELL {
				continue
			}
			if item.GetState() != pb.OperationState_OPERATION_STATE_EXECUTED {
				continue
			}
			snapshot, err := a.recoverOperation(ctx, item)
			if err != nil {
				return exchange.ExecutionHistory{}, err
			}
			if previous, exists := byOrder[snapshot.ExchangeOrderID]; exists {
				if !sameRecoveredOrder(previous, snapshot) {
					return exchange.ExecutionHistory{}, fmt.Errorf("sandbox execution history order %s changed within one scan", snapshot.ExchangeOrderID)
				}
				continue
			}
			byOrder[snapshot.ExchangeOrderID] = snapshot
		}
		if !response.GetHasNext() {
			result.Complete = true
			break
		}
		next := response.GetNextCursor()
		if next == "" || next == cursor {
			return exchange.ExecutionHistory{}, errors.New("sandbox execution history pagination did not advance")
		}
		if _, duplicate := seenCursors[next]; duplicate {
			return exchange.ExecutionHistory{}, errors.New("sandbox execution history pagination repeated a cursor")
		}
		seenCursors[next] = struct{}{}
		cursor = next
	}
	if !result.Complete {
		return exchange.ExecutionHistory{}, errors.New("sandbox execution history exceeded pagination safety limit")
	}
	result.Orders = make([]exchange.RecoveredOrderSnapshot, 0, len(byOrder))
	for _, order := range byOrder {
		result.Orders = append(result.Orders, order)
	}
	sort.Slice(result.Orders, func(i, j int) bool { return result.Orders[i].ExchangeOrderID < result.Orders[j].ExchangeOrderID })
	return result, nil
}

func (a *Adapter) recoverOperation(ctx context.Context, item *pb.OperationItem) (exchange.RecoveredOrderSnapshot, error) {
	if item == nil || item.GetId() == "" {
		return exchange.RecoveredOrderSnapshot{}, errors.New("sandbox execution history operation ID is missing")
	}
	if item.GetBrokerAccountId() != "" && item.GetBrokerAccountId() != a.accountID {
		return exchange.RecoveredOrderSnapshot{}, fmt.Errorf("sandbox execution history operation %s belongs to unexpected account", item.GetId())
	}
	wantSide := domain.OrderSideBuy
	if item.GetType() == pb.OperationType_OPERATION_TYPE_SELL {
		wantSide = domain.OrderSideSell
	} else if item.GetType() != pb.OperationType_OPERATION_TYPE_BUY {
		return exchange.RecoveredOrderSnapshot{}, fmt.Errorf("sandbox execution history operation %s has unexpected type %s", item.GetId(), item.GetType())
	}
	if item.GetState() != pb.OperationState_OPERATION_STATE_EXECUTED {
		return exchange.RecoveredOrderSnapshot{}, fmt.Errorf("sandbox execution history operation %s is not executed", item.GetId())
	}
	request := &pb.GetOrderStateRequest{AccountId: a.accountID, OrderId: item.GetId()}
	state, err := retryRead(ctx, "get sandbox history order state", a.timeout, a.readRetryPolicy(), func(callCtx context.Context) (*pb.OrderState, error) {
		return a.sandbox.GetSandboxOrderState(callCtx, request)
	})
	if err != nil {
		return exchange.RecoveredOrderSnapshot{}, err
	}
	if state == nil {
		return exchange.RecoveredOrderSnapshot{}, fmt.Errorf("sandbox execution history operation %s has no order state", item.GetId())
	}
	orderID := domain.OrderID(state.GetOrderId())
	clientID := domain.ClientOrderID(state.GetOrderRequestId())
	instrumentID := domain.InstrumentID(state.GetInstrumentUid())
	if orderID.Validate() != nil || clientID.Validate() != nil || instrumentID.Validate() != nil {
		return exchange.RecoveredOrderSnapshot{}, fmt.Errorf("sandbox execution history operation %s has invalid order identity", item.GetId())
	}
	if item.GetInstrumentUid() != "" && item.GetInstrumentUid() != string(instrumentID) {
		return exchange.RecoveredOrderSnapshot{}, fmt.Errorf("sandbox execution history operation %s changed instrument", item.GetId())
	}
	if orderSide(state.GetDirection()) != wantSide {
		return exchange.RecoveredOrderSnapshot{}, fmt.Errorf("sandbox execution history operation %s changed direction", item.GetId())
	}
	instrument, err := a.Instrument(ctx, instrumentID)
	if err != nil {
		return exchange.RecoveredOrderSnapshot{}, fmt.Errorf("sandbox execution history instrument: %w", err)
	}
	commission, err := money(state.GetExecutedCommission())
	if err != nil {
		return exchange.RecoveredOrderSnapshot{}, fmt.Errorf("sandbox execution history commission: %w", err)
	}
	status := mapOrderStatus(state.GetExecutionReportStatus())
	if status == domain.OrderStatusUnknown || status == domain.OrderStatusPending ||
		state.GetOrderDate() == nil || !state.GetOrderDate().IsValid() || state.GetLotsRequested() <= 0 ||
		(state.GetOrderType() != pb.OrderType_ORDER_TYPE_MARKET && state.GetOrderType() != pb.OrderType_ORDER_TYPE_LIMIT) {
		return exchange.RecoveredOrderSnapshot{}, fmt.Errorf("sandbox execution history order %s has invalid state", orderID)
	}
	snapshot := exchange.RecoveredOrderSnapshot{
		ExchangeOrderID: orderID, ClientOrderID: clientID, InstrumentID: instrumentID, Side: wantSide,
		OrderType:         mapOrderTypeFromProto(state.GetOrderType()),
		RequestedQuantity: lotsToQuantity(state.GetLotsRequested(), instrument.QuantityStep),
		Status:            status, SubmittedAt: state.GetOrderDate().AsTime().UTC(),
		CumulativeCommission: commission, Complete: historyOrderComplete(state.GetExecutionReportStatus()),
	}
	seenTrades := make(map[string]struct{}, len(state.GetStages()))
	var stageLots int64
	for _, stage := range state.GetStages() {
		if stage == nil || stage.GetTradeId() == "" || stage.GetQuantity() <= 0 || stage.GetExecutionTime() == nil || !stage.GetExecutionTime().IsValid() {
			return exchange.RecoveredOrderSnapshot{}, fmt.Errorf("sandbox execution history order %s has invalid stage", orderID)
		}
		if _, duplicate := seenTrades[stage.GetTradeId()]; duplicate {
			return exchange.RecoveredOrderSnapshot{}, fmt.Errorf("sandbox execution history order %s repeats trade %s", orderID, stage.GetTradeId())
		}
		seenTrades[stage.GetTradeId()] = struct{}{}
		stageLots += stage.GetQuantity()
		stageMoney, err := money(stage.GetPrice())
		if err != nil {
			return exchange.RecoveredOrderSnapshot{}, fmt.Errorf("sandbox execution history trade %s price: %w", stage.GetTradeId(), err)
		}
		if stageMoney.Asset != instrument.QuoteAsset {
			return exchange.RecoveredOrderSnapshot{}, fmt.Errorf("sandbox execution history trade %s price asset changed", stage.GetTradeId())
		}
		stagePrice := domain.Price{Value: stageMoney.Amount, Asset: stageMoney.Asset}
		snapshot.Fills = append(snapshot.Fills, exchange.RecoveredExecutionFill{
			TradeID: stage.GetTradeId(), Quantity: lotsToQuantity(stage.GetQuantity(), instrument.QuantityStep),
			Price: stagePrice, ExecutedAt: stage.GetExecutionTime().AsTime().UTC(),
		})
	}
	if state.GetLotsExecuted() > 0 && len(snapshot.Fills) == 0 {
		return exchange.RecoveredOrderSnapshot{}, fmt.Errorf("sandbox execution history order %s has executed lots without stages", orderID)
	}
	if stageLots != state.GetLotsExecuted() {
		return exchange.RecoveredOrderSnapshot{}, fmt.Errorf("sandbox execution history order %s stages cover %d lots, expected %d", orderID, stageLots, state.GetLotsExecuted())
	}
	sort.Slice(snapshot.Fills, func(i, j int) bool {
		if snapshot.Fills[i].ExecutedAt.Equal(snapshot.Fills[j].ExecutedAt) {
			return snapshot.Fills[i].TradeID < snapshot.Fills[j].TradeID
		}
		return snapshot.Fills[i].ExecutedAt.Before(snapshot.Fills[j].ExecutedAt)
	})
	return snapshot, nil
}

func historyOrderComplete(status pb.OrderExecutionReportStatus) bool {
	return status == pb.OrderExecutionReportStatus_EXECUTION_REPORT_STATUS_FILL ||
		status == pb.OrderExecutionReportStatus_EXECUTION_REPORT_STATUS_CANCELLED ||
		status == pb.OrderExecutionReportStatus_EXECUTION_REPORT_STATUS_REJECTED
}

func sameRecoveredOrder(a, b exchange.RecoveredOrderSnapshot) bool {
	if a.ExchangeOrderID != b.ExchangeOrderID || a.ClientOrderID != b.ClientOrderID || a.InstrumentID != b.InstrumentID || a.Side != b.Side ||
		a.OrderType != b.OrderType || !a.RequestedQuantity.Value.Equal(b.RequestedQuantity.Value) ||
		a.Status != b.Status || !a.SubmittedAt.Equal(b.SubmittedAt) || a.Complete != b.Complete ||
		a.CumulativeCommission.Asset != b.CumulativeCommission.Asset || !a.CumulativeCommission.Amount.Equal(b.CumulativeCommission.Amount) || len(a.Fills) != len(b.Fills) {
		return false
	}
	for i := range a.Fills {
		if a.Fills[i].TradeID != b.Fills[i].TradeID || !a.Fills[i].Quantity.Value.Equal(b.Fills[i].Quantity.Value) ||
			a.Fills[i].Price.Asset != b.Fills[i].Price.Asset || !a.Fills[i].Price.Value.Equal(b.Fills[i].Price.Value) || !a.Fills[i].ExecutedAt.Equal(b.Fills[i].ExecutedAt) {
			return false
		}
	}
	return true
}

func mapOrderTypeFromProto(value pb.OrderType) domain.OrderType {
	if value == pb.OrderType_ORDER_TYPE_MARKET {
		return domain.OrderTypeMarket
	}
	return domain.OrderTypeLimit
}

var _ exchange.ExecutionHistoryProvider = (*Adapter)(nil)
