package tinvest

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/damirm/lazytrade/internal/domain"
	"github.com/damirm/lazytrade/internal/exchange"
	"github.com/shopspring/decimal"
	"google.golang.org/grpc"
	pb "opensource.tbank.ru/invest/invest-go/proto"
)

type sandboxService interface {
	OpenSandboxAccount(context.Context, *pb.OpenSandboxAccountRequest, ...grpc.CallOption) (*pb.OpenSandboxAccountResponse, error)
	GetSandboxAccounts(context.Context, *pb.GetAccountsRequest, ...grpc.CallOption) (*pb.GetAccountsResponse, error)
	SandboxPayIn(context.Context, *pb.SandboxPayInRequest, ...grpc.CallOption) (*pb.SandboxPayInResponse, error)
	GetSandboxOperationsByCursor(context.Context, *pb.GetOperationsByCursorRequest, ...grpc.CallOption) (*pb.GetOperationsByCursorResponse, error)
	GetSandboxOrderState(context.Context, *pb.GetOrderStateRequest, ...grpc.CallOption) (*pb.OrderState, error)
}

type ordersService interface {
	PostOrder(context.Context, *pb.PostOrderRequest, ...grpc.CallOption) (*pb.PostOrderResponse, error)
	CancelOrder(context.Context, *pb.CancelOrderRequest, ...grpc.CallOption) (*pb.CancelOrderResponse, error)
	GetOrderState(context.Context, *pb.GetOrderStateRequest, ...grpc.CallOption) (*pb.OrderState, error)
	GetOrders(context.Context, *pb.GetOrdersRequest, ...grpc.CallOption) (*pb.GetOrdersResponse, error)
}

type operationsService interface {
	GetPortfolio(context.Context, *pb.PortfolioRequest, ...grpc.CallOption) (*pb.PortfolioResponse, error)
}

func (a *Adapter) PlaceOrder(ctx context.Context, request exchange.NewOrder) (domain.Order, error) {
	if err := request.Validate(); err != nil {
		return domain.Order{}, fmt.Errorf("place order: %w", err)
	}
	if err := a.validateAccount(request.ExchangeAccountID); err != nil {
		return domain.Order{}, err
	}
	direction, err := mapOrderDirection(request.Side)
	if err != nil {
		return domain.Order{}, err
	}
	apiOrderType, err := mapOrderType(request.Type)
	if err != nil {
		return domain.Order{}, err
	}
	instrument, err := a.Instrument(ctx, request.InstrumentID)
	if err != nil {
		return domain.Order{}, fmt.Errorf("place order metadata: %w", err)
	}
	lots, err := quantityToLots(request.Quantity, instrument.QuantityStep)
	if err != nil {
		return domain.Order{}, fmt.Errorf("place order quantity: %w", err)
	}
	apiRequest := &pb.PostOrderRequest{
		InstrumentId: string(request.InstrumentID), Quantity: lots,
		AccountId: a.accountID, OrderId: string(request.ClientOrderID),
		Direction: direction, OrderType: apiOrderType,
	}
	if request.LimitPrice != nil {
		apiRequest.Price = decimalQuotation(request.LimitPrice.Value)
	}
	cctx, cancel := a.timeoutContext(ctx)
	defer cancel()
	response, err := a.orders.PostOrder(cctx, apiRequest)
	if err != nil {
		return domain.Order{}, mapMutationError("place order", err)
	}
	if response == nil || response.GetOrderRequestId() != string(request.ClientOrderID) {
		return domain.Order{}, mutationResponseError("map placed order response", errors.New("response contains an unexpected client order ID"))
	}
	if response.GetLotsRequested() != lots {
		return domain.Order{}, mutationResponseError("map placed order response", errors.New("response contains an unexpected requested lot count"))
	}
	order, err := mapPostOrder(response, request, instrument.QuantityStep, time.Now().UTC())
	if err != nil {
		return domain.Order{}, mutationResponseError("map placed order response", err)
	}
	return order, nil
}

func (a *Adapter) CancelOrder(ctx context.Context, orderID domain.OrderID) error {
	if err := orderID.Validate(); err != nil {
		return fmt.Errorf("cancel order: %w", err)
	}
	if a.accountID == "" {
		return errors.New("cancel order: adapter account ID is required")
	}
	cctx, cancel := a.timeoutContext(ctx)
	defer cancel()
	response, err := a.orders.CancelOrder(cctx, &pb.CancelOrderRequest{
		AccountId: a.accountID, OrderId: string(orderID),
	})
	if err != nil {
		return mapMutationError("cancel order", err)
	}
	if _, err := requiredTime(response.GetTime()); err != nil {
		return mutationResponseError("map canceled order response", fmt.Errorf("cancellation time: %w", err))
	}
	return nil
}

func (a *Adapter) GetOrder(ctx context.Context, orderID domain.OrderID) (domain.Order, error) {
	return a.getOrder(ctx, string(orderID), pb.OrderIdType_ORDER_ID_TYPE_EXCHANGE)
}

func (a *Adapter) GetOrderByClientID(ctx context.Context, clientID domain.ClientOrderID) (domain.Order, error) {
	return a.getOrder(ctx, string(clientID), pb.OrderIdType_ORDER_ID_TYPE_REQUEST)
}

func (a *Adapter) getOrder(ctx context.Context, id string, idType pb.OrderIdType) (domain.Order, error) {
	if err := domain.ValidateID(id); err != nil {
		return domain.Order{}, fmt.Errorf("get order: %w", err)
	}
	if a.accountID == "" {
		return domain.Order{}, errors.New("get order: adapter account ID is required")
	}
	request := &pb.GetOrderStateRequest{
		AccountId: a.accountID, OrderId: id, OrderIdType: &idType,
	}
	state, err := retryRead(ctx, "get order", a.timeout, a.readRetryPolicy(), func(callCtx context.Context) (*pb.OrderState, error) {
		return a.orders.GetOrderState(callCtx, request)
	})
	if err != nil {
		return domain.Order{}, err
	}
	return a.mapOrderState(ctx, state)
}

func (a *Adapter) OpenOrders(ctx context.Context, accountID domain.ExchangeAccountID) ([]domain.Order, error) {
	if err := a.validateAccount(accountID); err != nil {
		return nil, err
	}
	request := &pb.GetOrdersRequest{AccountId: a.accountID}
	response, err := retryRead(ctx, "list orders", a.timeout, a.readRetryPolicy(), func(callCtx context.Context) (*pb.GetOrdersResponse, error) {
		return a.orders.GetOrders(callCtx, request)
	})
	if err != nil {
		return nil, err
	}
	if response == nil {
		return nil, errors.New("orders response is missing")
	}
	result := make([]domain.Order, 0, len(response.GetOrders()))
	for _, state := range response.GetOrders() {
		order, mapErr := a.mapOrderState(ctx, state)
		if mapErr != nil {
			return nil, fmt.Errorf("map order %q: %w", state.GetOrderId(), mapErr)
		}
		result = append(result, order)
	}
	return result, nil
}

func (a *Adapter) mapOrderState(ctx context.Context, state *pb.OrderState) (domain.Order, error) {
	if state == nil {
		return domain.Order{}, errors.New("order state is missing")
	}
	instrumentID := domain.InstrumentID(state.GetInstrumentUid())
	instrument, err := a.Instrument(ctx, instrumentID)
	if err != nil {
		return domain.Order{}, fmt.Errorf("order instrument: %w", err)
	}
	submitted, err := requiredTime(state.GetOrderDate())
	if err != nil {
		return domain.Order{}, fmt.Errorf("order date: %w", err)
	}
	side, err := orderSide(state.GetDirection())
	if err != nil {
		return domain.Order{}, err
	}
	typ, err := orderType(state.GetOrderType())
	if err != nil {
		return domain.Order{}, err
	}
	status, err := mapOrderStatus(state.GetExecutionReportStatus())
	if err != nil {
		return domain.Order{}, err
	}
	var limitPrice *domain.Price
	if state.GetOrderType() == pb.OrderType_ORDER_TYPE_LIMIT && state.GetInitialSecurityPrice() != nil {
		mapped, mapErr := price(
			&pb.Quotation{Units: state.GetInitialSecurityPrice().GetUnits(), Nano: state.GetInitialSecurityPrice().GetNano()},
			state.GetInitialSecurityPrice().GetCurrency(),
		)
		if mapErr != nil {
			return domain.Order{}, mapErr
		}
		limitPrice = &mapped
	}
	order := domain.Order{
		ID: domain.OrderID(state.GetOrderId()), ClientOrderID: domain.ClientOrderID(state.GetOrderRequestId()),
		StrategyID: "external", ExchangeAccountID: domain.ExchangeAccountID(a.name),
		InstrumentID: instrumentID, Side: side, Type: typ,
		Status:         status,
		Quantity:       lotsToQuantity(state.GetLotsRequested(), instrument.QuantityStep),
		FilledQuantity: lotsToQuantity(state.GetLotsExecuted(), instrument.QuantityStep),
		LimitPrice:     limitPrice, SubmittedAt: submitted, UpdatedAt: submitted,
	}
	return order, order.Validate()
}

func mapPostOrder(response *pb.PostOrderResponse, request exchange.NewOrder, lotSize domain.Quantity, now time.Time) (domain.Order, error) {
	if response == nil {
		return domain.Order{}, errors.New("post order response is missing")
	}
	status, err := mapOrderStatus(response.GetExecutionReportStatus())
	if err != nil {
		return domain.Order{}, err
	}
	order := domain.Order{
		ID: domain.OrderID(response.GetOrderId()), ClientOrderID: request.ClientOrderID,
		StrategyID: request.StrategyID, ExchangeAccountID: request.ExchangeAccountID,
		InstrumentID: request.InstrumentID, Side: request.Side, Type: request.Type,
		Status:         status,
		Quantity:       lotsToQuantity(response.GetLotsRequested(), lotSize),
		FilledQuantity: lotsToQuantity(response.GetLotsExecuted(), lotSize),
		LimitPrice:     request.LimitPrice, SubmittedAt: now.UTC(), UpdatedAt: now.UTC(),
	}
	return order, order.Validate()
}

func (a *Adapter) validateAccount(accountID domain.ExchangeAccountID) error {
	if err := accountID.Validate(); err != nil {
		return fmt.Errorf("exchange account ID: %w", err)
	}
	if string(accountID) != a.name {
		return fmt.Errorf("exchange account alias %q does not match adapter %q", accountID, a.name)
	}
	if a.accountID == "" {
		return errors.New("T-Invest account ID is not configured")
	}
	return nil
}

func quantityToLots(quantity, lotSize domain.Quantity) (int64, error) {
	if err := quantity.Validate(); err != nil {
		return 0, err
	}
	if err := lotSize.Validate(); err != nil || !lotSize.Value.IsPositive() {
		return 0, errors.New("invalid instrument lot size")
	}
	lots := quantity.Value.Div(lotSize.Value)
	if !lots.IsPositive() || !lots.Equal(lots.Truncate(0)) {
		return 0, fmt.Errorf("quantity %s is not a positive whole number of lots (lot size %s)", quantity.Value, lotSize.Value)
	}
	return lots.IntPart(), nil
}

func lotsToQuantity(lots int64, lotSize domain.Quantity) domain.Quantity {
	return domain.Quantity{Value: decimal.NewFromInt(lots).Mul(lotSize.Value)}
}

func decimalQuotation(value decimal.Decimal) *pb.Quotation {
	units := value.Truncate(0).IntPart()
	nanos := value.Sub(decimal.NewFromInt(units)).Shift(9).IntPart()
	return &pb.Quotation{Units: units, Nano: int32(nanos)}
}

func mapOrderDirection(side domain.OrderSide) (pb.OrderDirection, error) {
	switch side {
	case domain.OrderSideBuy:
		return pb.OrderDirection_ORDER_DIRECTION_BUY, nil
	case domain.OrderSideSell:
		return pb.OrderDirection_ORDER_DIRECTION_SELL, nil
	default:
		return pb.OrderDirection_ORDER_DIRECTION_UNSPECIFIED, fmt.Errorf("unsupported order side %d", side)
	}
}

func mapOrderType(value domain.OrderType) (pb.OrderType, error) {
	switch value {
	case domain.OrderTypeMarket:
		return pb.OrderType_ORDER_TYPE_MARKET, nil
	case domain.OrderTypeLimit:
		return pb.OrderType_ORDER_TYPE_LIMIT, nil
	default:
		return pb.OrderType_ORDER_TYPE_UNSPECIFIED, fmt.Errorf("unsupported order type %d", value)
	}
}

func orderSide(value pb.OrderDirection) (domain.OrderSide, error) {
	switch value {
	case pb.OrderDirection_ORDER_DIRECTION_BUY:
		return domain.OrderSideBuy, nil
	case pb.OrderDirection_ORDER_DIRECTION_SELL:
		return domain.OrderSideSell, nil
	default:
		return 0, fmt.Errorf("unsupported order direction %s", value)
	}
}

func orderType(value pb.OrderType) (domain.OrderType, error) {
	switch value {
	case pb.OrderType_ORDER_TYPE_MARKET:
		return domain.OrderTypeMarket, nil
	case pb.OrderType_ORDER_TYPE_LIMIT:
		return domain.OrderTypeLimit, nil
	default:
		return 0, fmt.Errorf("unsupported order type %s", value)
	}
}

func mapOrderStatus(value pb.OrderExecutionReportStatus) (domain.OrderStatus, error) {
	switch value {
	case pb.OrderExecutionReportStatus_EXECUTION_REPORT_STATUS_NEW:
		return domain.OrderStatusAccepted, nil
	case pb.OrderExecutionReportStatus_EXECUTION_REPORT_STATUS_PARTIALLYFILL:
		return domain.OrderStatusPartiallyFilled, nil
	case pb.OrderExecutionReportStatus_EXECUTION_REPORT_STATUS_FILL:
		return domain.OrderStatusFilled, nil
	case pb.OrderExecutionReportStatus_EXECUTION_REPORT_STATUS_CANCELLED:
		return domain.OrderStatusCancelled, nil
	case pb.OrderExecutionReportStatus_EXECUTION_REPORT_STATUS_REJECTED:
		return domain.OrderStatusRejected, nil
	default:
		return 0, fmt.Errorf("unsupported order status %s", value)
	}
}
