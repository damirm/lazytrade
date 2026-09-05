package backtest

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"sort"
	"time"

	"github.com/damirm/lazytrade/internal/domain"
	"github.com/shopspring/decimal"
)

const FillModelVersion = "candle-next-open-touch/v1"

var (
	ErrPriceNotAligned      = errors.New("price is not aligned to tick size")
	ErrQuantityNotAligned   = errors.New("quantity is not aligned to lot size")
	ErrInsufficientFunds    = errors.New("insufficient funds")
	ErrInsufficientPosition = errors.New("insufficient position")
)

type BrokerConfig struct {
	InitialCash       domain.Money
	InstrumentID      domain.InstrumentID
	TickSize          domain.Price
	LotSize           domain.Quantity
	CommissionPercent decimal.Decimal
	SlippageBPS       decimal.Decimal
}

func (c BrokerConfig) Validate() error {
	if err := c.InitialCash.Validate(); err != nil || c.InitialCash.Amount.IsNegative() {
		return errors.New("initial cash must be non-negative")
	}
	if err := c.InstrumentID.Validate(); err != nil {
		return fmt.Errorf("instrument ID: %w", err)
	}
	if err := c.TickSize.Validate(); err != nil || c.TickSize.Asset != c.InitialCash.Asset {
		return errors.New("tick size must be positive and use the cash asset")
	}
	if err := c.LotSize.Validate(); err != nil || !c.LotSize.Value.IsPositive() {
		return errors.New("lot size must be positive")
	}
	if c.CommissionPercent.IsNegative() || c.SlippageBPS.IsNegative() {
		return errors.New("commission and slippage must be non-negative")
	}
	if c.CommissionPercent.GreaterThan(decimal.NewFromInt(100)) ||
		c.SlippageBPS.GreaterThanOrEqual(decimal.NewFromInt(10000)) {
		return errors.New("commission must not exceed 100% and slippage must be below 10000 BPS")
	}
	return nil
}

type SubmitRequest struct {
	OrderID         domain.OrderID
	ClientOrderID   domain.ClientOrderID
	StrategyID      domain.StrategyID
	InstrumentID    domain.InstrumentID
	Side            domain.OrderSide
	Type            domain.OrderType
	Quantity        domain.Quantity
	LimitPrice      *domain.Price
	SubmittedAt     time.Time
	CausativeCursor domain.EventCursor
}

type PortfolioSnapshot struct {
	Cash             domain.Money
	PositionQuantity decimal.Decimal
	LastPrice        *domain.Price
	Equity           domain.Money
	Commissions      domain.Money
	SlippageCost     domain.Money
}

type SimulatedBroker struct {
	config       BrokerConfig
	cash         decimal.Decimal
	position     decimal.Decimal
	orders       map[domain.OrderID]domain.Order
	orderPayload map[domain.ClientOrderID]SubmitRequest
	commissions  decimal.Decimal
	slippageCost decimal.Decimal
	lastPrice    *domain.Price
	fills        uint64
}

func NewSimulatedBroker(config BrokerConfig) (*SimulatedBroker, error) {
	if err := config.Validate(); err != nil {
		return nil, err
	}
	return &SimulatedBroker{
		config: config, cash: config.InitialCash.Amount,
		orders:       make(map[domain.OrderID]domain.Order),
		orderPayload: make(map[domain.ClientOrderID]SubmitRequest),
	}, nil
}

func (b *SimulatedBroker) Submit(_ context.Context, request SubmitRequest) (domain.Order, error) {
	if prior, ok := b.orderPayload[request.ClientOrderID]; ok {
		if equalRequests(prior, request) {
			return b.orders[prior.OrderID], nil
		}
		return domain.Order{}, errors.New("client order ID conflict")
	}
	if err := validateRequest(request, b.config); err != nil {
		return domain.Order{}, err
	}
	order := domain.Order{
		ID: request.OrderID, ClientOrderID: request.ClientOrderID, StrategyID: request.StrategyID,
		InstrumentID: request.InstrumentID, ExchangeAccountID: "backtest",
		Side: request.Side, Type: request.Type, Status: domain.OrderStatusAccepted,
		Quantity: request.Quantity, LimitPrice: request.LimitPrice,
		SubmittedAt: request.SubmittedAt.UTC(), UpdatedAt: request.SubmittedAt.UTC(),
	}
	b.orders[order.ID] = order
	b.orderPayload[order.ClientOrderID] = request
	return order, nil
}

func (b *SimulatedBroker) OnMarketEvent(_ context.Context, event domain.MarketEvent) ([]domain.Execution, error) {
	if event.Kind != domain.MarketEventCandleClose || event.Candle == nil {
		return nil, nil
	}
	if event.InstrumentID != b.config.InstrumentID {
		return nil, errors.New("event instrument does not match broker")
	}
	candle := event.Candle
	if err := candle.Validate(); err != nil {
		return nil, fmt.Errorf("market candle: %w", err)
	}
	for _, value := range []domain.Price{candle.Open, candle.High, candle.Low, candle.Close} {
		if value.Asset != b.config.InitialCash.Asset {
			return nil, domain.ErrAssetMismatch
		}
		if !value.Value.Mod(b.config.TickSize.Value).IsZero() {
			return nil, ErrPriceNotAligned
		}
	}
	if !candle.Volume.Value.Mod(b.config.LotSize.Value).IsZero() {
		return nil, ErrQuantityNotAligned
	}
	b.lastPrice = &candle.Close
	ids := make([]string, 0, len(b.orders))
	for id, order := range b.orders {
		if order.Status == domain.OrderStatusAccepted {
			ids = append(ids, string(id))
		}
	}
	sort.Strings(ids)
	executions := make([]domain.Execution, 0, len(ids))
	for _, rawID := range ids {
		order := b.orders[domain.OrderID(rawID)]
		request := b.orderPayload[order.ClientOrderID]
		// A signal produced by candle N has SubmittedAt == N.End. It first
		// becomes eligible at the open of a candle starting at or after N.End.
		if candle.Start.Before(request.CausativeCursor.Timestamp) {
			continue
		}
		raw, fill := rawFill(order, *candle)
		if !fill {
			continue
		}
		execution, fillErr := b.fill(order, raw, candle.Start)
		if fillErr != nil {
			if !errors.Is(fillErr, ErrInsufficientFunds) && !errors.Is(fillErr, ErrInsufficientPosition) {
				return nil, fillErr
			}
			order.Status = domain.OrderStatusRejected
			order.UpdatedAt = candle.Start.UTC()
			b.orders[order.ID] = order
			continue
		}
		order.Status = domain.OrderStatusFilled
		order.FilledQuantity = order.Quantity
		order.UpdatedAt = candle.Start
		b.orders[order.ID] = order
		executions = append(executions, execution)
	}
	return executions, nil
}

func rawFill(order domain.Order, candle domain.Candle) (domain.Price, bool) {
	if order.Type == domain.OrderTypeMarket {
		return candle.Open, true
	}
	limit := *order.LimitPrice
	if order.Side == domain.OrderSideBuy {
		if candle.Low.Value.GreaterThan(limit.Value) {
			return domain.Price{}, false
		}
		if candle.Open.Value.LessThan(limit.Value) {
			return candle.Open, true
		}
		return limit, true
	}
	if candle.High.Value.LessThan(limit.Value) {
		return domain.Price{}, false
	}
	if candle.Open.Value.GreaterThan(limit.Value) {
		return candle.Open, true
	}
	return limit, true
}

func (b *SimulatedBroker) fill(order domain.Order, raw domain.Price, at time.Time) (domain.Execution, error) {
	fraction := b.config.SlippageBPS.Div(decimal.NewFromInt(10000))
	delta := raw.Value.Mul(fraction)
	price := raw.Value
	if order.Side == domain.OrderSideBuy {
		price = price.Add(delta)
	} else {
		price = price.Sub(delta)
	}
	price = adverseRound(price, b.config.TickSize.Value, order.Side)
	if !price.IsPositive() {
		return domain.Execution{}, errors.New("slippage produced a non-positive fill price")
	}
	notional := price.Mul(order.Quantity.Value)
	commission := notional.Abs().Mul(b.config.CommissionPercent).Div(decimal.NewFromInt(100))
	slippage := price.Sub(raw.Value).Mul(order.Quantity.Value).Abs()
	if order.Side == domain.OrderSideBuy {
		if b.cash.LessThan(notional.Add(commission)) {
			return domain.Execution{}, ErrInsufficientFunds
		}
		b.cash = b.cash.Sub(notional).Sub(commission)
		b.position = b.position.Add(order.Quantity.Value)
	} else {
		if b.position.LessThan(order.Quantity.Value) {
			return domain.Execution{}, ErrInsufficientPosition
		}
		b.cash = b.cash.Add(notional).Sub(commission)
		b.position = b.position.Sub(order.Quantity.Value)
	}
	b.commissions = b.commissions.Add(commission)
	b.slippageCost = b.slippageCost.Add(slippage)
	b.fills++
	sum := sha256.Sum256([]byte(fmt.Sprintf("execution/v1:%s:%d", order.ID, b.fills)))
	return domain.Execution{
		ID: domain.ExecutionID(hex.EncodeToString(sum[:])), OrderID: order.ID,
		StrategyID: order.StrategyID, InstrumentID: order.InstrumentID, Side: order.Side,
		Quantity: order.Quantity, Price: domain.Price{Value: price, Asset: raw.Asset},
		Commission: domain.Money{Amount: commission, Asset: raw.Asset}, ExecutedAt: at.UTC(),
	}, nil
}

func adverseRound(value, step decimal.Decimal, side domain.OrderSide) decimal.Decimal {
	units := value.Div(step)
	if side == domain.OrderSideBuy {
		return units.Ceil().Mul(step)
	}
	return units.Floor().Mul(step)
}

func (b *SimulatedBroker) Snapshot() PortfolioSnapshot {
	equity := b.cash
	if b.lastPrice != nil {
		equity = equity.Add(b.position.Mul(b.lastPrice.Value))
	}
	return PortfolioSnapshot{
		Cash:             domain.Money{Amount: b.cash, Asset: b.config.InitialCash.Asset},
		PositionQuantity: b.position, LastPrice: b.lastPrice,
		Equity:       domain.Money{Amount: equity, Asset: b.config.InitialCash.Asset},
		Commissions:  domain.Money{Amount: b.commissions, Asset: b.config.InitialCash.Asset},
		SlippageCost: domain.Money{Amount: b.slippageCost, Asset: b.config.InitialCash.Asset},
	}
}

func (b *SimulatedBroker) Orders() []domain.Order {
	result := make([]domain.Order, 0, len(b.orders))
	for _, order := range b.orders {
		result = append(result, order)
	}
	sort.Slice(result, func(i, j int) bool {
		if result[i].SubmittedAt.Equal(result[j].SubmittedAt) {
			return result[i].ID < result[j].ID
		}
		return result[i].SubmittedAt.Before(result[j].SubmittedAt)
	})
	return result
}

func validateRequest(r SubmitRequest, config BrokerConfig) error {
	if err := r.OrderID.Validate(); err != nil {
		return err
	}
	if err := r.ClientOrderID.Validate(); err != nil {
		return err
	}
	if err := r.StrategyID.Validate(); err != nil {
		return err
	}
	if r.InstrumentID != config.InstrumentID {
		return errors.New("order instrument does not match broker")
	}
	if r.Side != domain.OrderSideBuy && r.Side != domain.OrderSideSell {
		return errors.New("invalid side")
	}
	if err := r.Quantity.Validate(); err != nil || !r.Quantity.Value.IsPositive() {
		return errors.New("quantity must be positive")
	}
	if !r.Quantity.Value.Mod(config.LotSize.Value).IsZero() {
		return ErrQuantityNotAligned
	}
	if r.SubmittedAt.IsZero() || r.SubmittedAt.Location() != time.UTC {
		return errors.New("submitted time must be UTC")
	}
	if r.CausativeCursor.Timestamp.IsZero() {
		return errors.New("causative cursor is required")
	}
	if r.CausativeCursor.Timestamp.Location() != time.UTC ||
		r.SubmittedAt.Before(r.CausativeCursor.Timestamp) {
		return errors.New("causative cursor must be UTC and not after submitted time")
	}
	if r.Type == domain.OrderTypeMarket && r.LimitPrice != nil {
		return errors.New("market order cannot have limit")
	}
	if r.Type == domain.OrderTypeLimit && r.LimitPrice == nil {
		return errors.New("limit order requires limit")
	}
	if r.Type != domain.OrderTypeMarket && r.Type != domain.OrderTypeLimit {
		return errors.New("invalid order type")
	}
	if r.LimitPrice != nil {
		if err := r.LimitPrice.Validate(); err != nil {
			return fmt.Errorf("limit price: %w", err)
		}
		if r.LimitPrice.Asset != config.InitialCash.Asset {
			return domain.ErrAssetMismatch
		}
		if !r.LimitPrice.Value.Mod(config.TickSize.Value).IsZero() {
			return ErrPriceNotAligned
		}
	}
	return nil
}

func equalRequests(a, b SubmitRequest) bool {
	if a.OrderID != b.OrderID || a.ClientOrderID != b.ClientOrderID || a.StrategyID != b.StrategyID ||
		a.InstrumentID != b.InstrumentID || a.Side != b.Side || a.Type != b.Type ||
		!a.Quantity.Value.Equal(b.Quantity.Value) || !a.SubmittedAt.Equal(b.SubmittedAt) ||
		a.CausativeCursor != b.CausativeCursor {
		return false
	}
	if a.LimitPrice == nil || b.LimitPrice == nil {
		return a.LimitPrice == nil && b.LimitPrice == nil
	}
	return a.LimitPrice.Asset == b.LimitPrice.Asset && a.LimitPrice.Value.Equal(b.LimitPrice.Value)
}
