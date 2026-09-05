package tinvest

import (
	"context"
	"errors"
	"fmt"
	"sync/atomic"
	"time"

	"github.com/damirm/lazytrade/internal/domain"
	"github.com/damirm/lazytrade/internal/exchange"
	"github.com/shopspring/decimal"
	"google.golang.org/protobuf/types/known/timestamppb"
	pb "opensource.tbank.ru/invest/invest-go/proto"
)

type CandleQuery = exchange.CandleQuery

func candleInterval(d time.Duration) (pb.CandleInterval, error) {
	switch d {
	case time.Minute:
		return pb.CandleInterval_CANDLE_INTERVAL_1_MIN, nil
	case 5 * time.Minute:
		return pb.CandleInterval_CANDLE_INTERVAL_5_MIN, nil
	case 15 * time.Minute:
		return pb.CandleInterval_CANDLE_INTERVAL_15_MIN, nil
	case time.Hour:
		return pb.CandleInterval_CANDLE_INTERVAL_HOUR, nil
	case 24 * time.Hour:
		return pb.CandleInterval_CANDLE_INTERVAL_DAY, nil
	default:
		return 0, fmt.Errorf("unsupported candle interval %s", d)
	}
}

func (a *Adapter) Candles(ctx context.Context, q CandleQuery) ([]domain.Candle, error) {
	interval, err := candleInterval(q.Interval)
	if err != nil {
		return nil, err
	}
	request := &pb.GetCandlesRequest{
		InstrumentId: ptr(string(q.InstrumentID)), From: timestamppb.New(q.From),
		To: timestamppb.New(q.To), Interval: interval, Limit: ptr(q.Limit),
	}
	resp, err := retryRead(ctx, "get candles", a.timeout, a.readRetryPolicy(), func(callCtx context.Context) (*pb.GetCandlesResponse, error) {
		return a.market.GetCandles(callCtx, request)
	})
	if err != nil {
		return nil, err
	}
	result := make([]domain.Candle, 0, len(resp.Candles))
	for _, c := range resp.Candles {
		start := utc(c.Time.AsTime())
		item := domain.Candle{Start: start, End: start.Add(q.Interval), Interval: q.Interval,
			Volume: domain.Quantity{Value: decimal.NewFromInt(c.Volume)}, Complete: c.IsComplete}
		for source, target := range map[*pb.Quotation]*domain.Price{c.Open: &item.Open, c.High: &item.High, c.Low: &item.Low, c.Close: &item.Close} {
			*target, err = price(source, q.Asset)
			if err != nil {
				return nil, err
			}
		}
		if err = item.Validate(); err != nil {
			return nil, err
		}
		result = append(result, item)
	}
	return result, nil
}

func (a *Adapter) LastPrices(ctx context.Context, ids []domain.InstrumentID, asset string) (map[domain.InstrumentID]domain.Price, error) {
	raw := make([]string, len(ids))
	for i := range ids {
		raw[i] = string(ids[i])
	}
	request := &pb.GetLastPricesRequest{InstrumentId: raw}
	resp, err := retryRead(ctx, "get last prices", a.timeout, a.readRetryPolicy(), func(callCtx context.Context) (*pb.GetLastPricesResponse, error) {
		return a.market.GetLastPrices(callCtx, request)
	})
	if err != nil {
		return nil, err
	}
	result := make(map[domain.InstrumentID]domain.Price, len(resp.LastPrices))
	for _, item := range resp.LastPrices {
		p, mapErr := price(item.Price, asset)
		if mapErr != nil {
			return nil, mapErr
		}
		result[domain.InstrumentID(item.InstrumentUid)] = p
	}
	return result, nil
}

func (a *Adapter) OrderBook(ctx context.Context, id domain.InstrumentID, asset string, depth int) (domain.OrderBook, error) {
	request := &pb.GetOrderBookRequest{InstrumentId: ptr(string(id)), Depth: int32(depth)}
	resp, err := retryRead(ctx, "get order book", a.timeout, a.readRetryPolicy(), func(callCtx context.Context) (*pb.GetOrderBookResponse, error) {
		return a.market.GetOrderBook(callCtx, request)
	})
	if err != nil {
		return domain.OrderBook{}, err
	}
	result := domain.OrderBook{Depth: depth}
	for _, side := range []struct {
		src []*pb.Order
		dst *[]domain.OrderBookLevel
	}{{resp.Bids, &result.Bids}, {resp.Asks, &result.Asks}} {
		for _, level := range side.src {
			p, mapErr := price(level.Price, asset)
			if mapErr != nil {
				return result, mapErr
			}
			*side.dst = append(*side.dst, domain.OrderBookLevel{Price: p, Quantity: domain.Quantity{Value: decimal.NewFromInt(level.Quantity)}})
		}
	}
	return result, result.Validate()
}

func (a *Adapter) LastTrades(ctx context.Context, id domain.InstrumentID, asset string, from, to time.Time) ([]domain.MarketTrade, error) {
	request := &pb.GetLastTradesRequest{InstrumentId: ptr(string(id)), From: timestamppb.New(from), To: timestamppb.New(to)}
	resp, err := retryRead(ctx, "get last trades", a.timeout, a.readRetryPolicy(), func(callCtx context.Context) (*pb.GetLastTradesResponse, error) {
		return a.market.GetLastTrades(callCtx, request)
	})
	if err != nil {
		return nil, err
	}
	result := make([]domain.MarketTrade, 0, len(resp.Trades))
	for _, item := range resp.Trades {
		p, mapErr := price(item.Price, asset)
		if mapErr != nil {
			return nil, mapErr
		}
		side := domain.OrderSideBuy
		if item.Direction == pb.TradeDirection_TRADE_DIRECTION_SELL {
			side = domain.OrderSideSell
		}
		id := fmt.Sprintf("%s:%d:%d:%s", item.InstrumentUid, item.Time.AsTime().UnixNano(), item.Quantity, item.Direction.String())
		result = append(result, domain.MarketTrade{ID: id, Price: p, Quantity: domain.Quantity{Value: decimal.NewFromInt(item.Quantity)}, Side: side})
	}
	return result, nil
}

func (a *Adapter) TradingStatus(ctx context.Context, id domain.InstrumentID) (domain.TradingStatus, error) {
	request := &pb.GetTradingStatusRequest{InstrumentId: ptr(string(id))}
	resp, err := retryRead(ctx, "get trading status", a.timeout, a.readRetryPolicy(), func(callCtx context.Context) (*pb.GetTradingStatusResponse, error) {
		return a.market.GetTradingStatus(callCtx, request)
	})
	if err != nil {
		return 0, err
	}
	return mapStatus(resp.TradingStatus), nil
}

func (a *Adapter) SubscribeMarketData(ctx context.Context, subscriptions []exchange.Subscription) (exchange.MarketStream, error) {
	for i, s := range subscriptions {
		if err := s.Validate(); err != nil {
			return exchange.MarketStream{}, fmt.Errorf("subscription %d: %w", i, err)
		}
	}
	events := make(chan domain.MarketEvent, 128)
	errs := make(chan error, 8)
	states := make(chan exchange.StreamEvent, 8)
	go a.runStream(ctx, subscriptions, events, errs, states)
	return exchange.MarketStream{Events: events, Errors: errs, State: states}, nil
}

func (a *Adapter) runStream(ctx context.Context, desired []exchange.Subscription, events chan<- domain.MarketEvent, errs chan<- error, states chan<- exchange.StreamEvent) {
	defer close(events)
	defer close(errs)
	defer close(states)
	var generation uint64
	assets, err := a.subscriptionAssets(ctx, desired)
	if err != nil {
		errs <- err
		states <- exchange.StreamEvent{State: exchange.StreamClosed}
		return
	}
	for attempt := uint(0); ; attempt++ {
		if ctx.Err() != nil {
			states <- exchange.StreamEvent{State: exchange.StreamClosed, Generation: generation}
			return
		}
		generation++
		states <- exchange.StreamEvent{State: exchange.StreamConnecting, Generation: generation, Subscriptions: desired}
		stream, err := a.marketStream.MarketDataStream(ctx)
		if err == nil {
			err = sendSubscriptions(stream, desired)
		}
		if err == nil {
			states <- exchange.StreamEvent{State: exchange.StreamHealthy, Generation: generation, Subscriptions: desired}
			err = a.receive(ctx, stream, assets, events)
		}
		if ctx.Err() != nil {
			states <- exchange.StreamEvent{State: exchange.StreamClosed, Generation: generation}
			return
		}
		select {
		case errs <- mapError("market data stream", err):
		default:
		}
		states <- exchange.StreamEvent{State: exchange.StreamDisconnected, Generation: generation, Subscriptions: desired}
		timer := time.NewTimer(backoff(attempt))
		select {
		case <-ctx.Done():
			timer.Stop()
		case <-timer.C:
			continue
		}
	}
}

func (a *Adapter) subscriptionAssets(ctx context.Context, desired []exchange.Subscription) (map[domain.InstrumentID]string, error) {
	result := make(map[domain.InstrumentID]string)
	for _, subscription := range desired {
		if _, ok := result[subscription.InstrumentID]; ok {
			continue
		}
		instrument, err := a.Instrument(ctx, subscription.InstrumentID)
		if err != nil {
			return nil, err
		}
		result[subscription.InstrumentID] = instrument.QuoteAsset
	}
	return result, nil
}

func sendSubscriptions(stream pb.MarketDataStreamService_MarketDataStreamClient, desired []exchange.Subscription) error {
	for _, s := range desired {
		var request *pb.MarketDataRequest
		switch s.Kind {
		case exchange.SubscriptionCandles:
			interval, err := subscriptionInterval(s.Interval)
			if err != nil {
				return err
			}
			request = &pb.MarketDataRequest{Payload: &pb.MarketDataRequest_SubscribeCandlesRequest{SubscribeCandlesRequest: &pb.SubscribeCandlesRequest{SubscriptionAction: pb.SubscriptionAction_SUBSCRIPTION_ACTION_SUBSCRIBE, WaitingClose: true, Instruments: []*pb.CandleInstrument{{InstrumentId: string(s.InstrumentID), Interval: interval}}}}}
		case exchange.SubscriptionOrderBook:
			request = &pb.MarketDataRequest{Payload: &pb.MarketDataRequest_SubscribeOrderBookRequest{SubscribeOrderBookRequest: &pb.SubscribeOrderBookRequest{SubscriptionAction: pb.SubscriptionAction_SUBSCRIPTION_ACTION_SUBSCRIBE, Instruments: []*pb.OrderBookInstrument{{InstrumentId: string(s.InstrumentID), Depth: int32(s.Depth)}}}}}
		case exchange.SubscriptionTrades:
			request = &pb.MarketDataRequest{Payload: &pb.MarketDataRequest_SubscribeTradesRequest{SubscribeTradesRequest: &pb.SubscribeTradesRequest{SubscriptionAction: pb.SubscriptionAction_SUBSCRIPTION_ACTION_SUBSCRIBE, Instruments: []*pb.TradeInstrument{{InstrumentId: string(s.InstrumentID)}}}}}
		case exchange.SubscriptionLastPrice:
			request = &pb.MarketDataRequest{Payload: &pb.MarketDataRequest_SubscribeLastPriceRequest{SubscribeLastPriceRequest: &pb.SubscribeLastPriceRequest{SubscriptionAction: pb.SubscriptionAction_SUBSCRIPTION_ACTION_SUBSCRIBE, Instruments: []*pb.LastPriceInstrument{{InstrumentId: string(s.InstrumentID)}}}}}
		case exchange.SubscriptionTradingStatus:
			request = &pb.MarketDataRequest{Payload: &pb.MarketDataRequest_SubscribeInfoRequest{SubscribeInfoRequest: &pb.SubscribeInfoRequest{SubscriptionAction: pb.SubscriptionAction_SUBSCRIPTION_ACTION_SUBSCRIBE, Instruments: []*pb.InfoInstrument{{InstrumentId: string(s.InstrumentID)}}}}}
		}
		if err := stream.Send(request); err != nil {
			return err
		}
	}
	return nil
}

func (a *Adapter) receive(ctx context.Context, stream pb.MarketDataStreamService_MarketDataStreamClient, assets map[domain.InstrumentID]string, events chan<- domain.MarketEvent) error {
	var sequence atomic.Uint64
	for {
		resp, err := stream.Recv()
		if err != nil {
			return err
		}
		now := time.Now().UTC()
		event, err := mapStreamResponse(resp, assets)
		if err != nil {
			return err
		}
		if event == nil {
			continue
		}
		event.ExchangeAccountID = domain.ExchangeAccountID(a.name)
		event.ReceivedTime = now
		event.Sequence = sequence.Add(1)
		select {
		case events <- *event:
		case <-ctx.Done():
			return ctx.Err()
		}
	}
}

func mapStreamResponse(resp *pb.MarketDataResponse, assets map[domain.InstrumentID]string) (*domain.MarketEvent, error) {
	var event *domain.MarketEvent
	switch {
	case resp.GetCandle() != nil:
		v := resp.GetCandle()
		id := domain.InstrumentID(v.InstrumentUid)
		interval, mapErr := streamCandleInterval(v.Interval)
		if mapErr != nil {
			return nil, mapErr
		}
		start := utc(v.Time.AsTime())
		candle := domain.Candle{Start: start, End: start.Add(interval), Interval: interval,
			Volume: domain.Quantity{Value: decimal.NewFromInt(v.Volume)}, Complete: true}
		for source, target := range map[*pb.Quotation]*domain.Price{v.Open: &candle.Open, v.High: &candle.High, v.Low: &candle.Low, v.Close: &candle.Close} {
			*target, mapErr = price(source, assets[id])
			if mapErr != nil {
				return nil, mapErr
			}
		}
		// A candle-close event becomes observable at the end of its interval.
		// Using the start here would make live strategy cursors and trading-day
		// attribution differ from backtests.
		event = &domain.MarketEvent{InstrumentID: id, Kind: domain.MarketEventCandleClose, ExchangeTime: candle.End, Candle: &candle}
	case resp.GetOrderbook() != nil:
		v := resp.GetOrderbook()
		id := domain.InstrumentID(v.InstrumentUid)
		book := domain.OrderBook{Depth: int(v.Depth)}
		for _, side := range []struct {
			src []*pb.Order
			dst *[]domain.OrderBookLevel
		}{{v.Bids, &book.Bids}, {v.Asks, &book.Asks}} {
			for _, level := range side.src {
				p, mapErr := price(level.Price, assets[id])
				if mapErr != nil {
					return nil, mapErr
				}
				*side.dst = append(*side.dst, domain.OrderBookLevel{Price: p, Quantity: domain.Quantity{Value: decimal.NewFromInt(level.Quantity)}})
			}
		}
		event = &domain.MarketEvent{InstrumentID: id, Kind: domain.MarketEventOrderBook, ExchangeTime: utc(v.Time.AsTime()), OrderBook: &book}
	case resp.GetTrade() != nil:
		v := resp.GetTrade()
		id := domain.InstrumentID(v.InstrumentUid)
		p, mapErr := price(v.Price, assets[id])
		if mapErr != nil {
			return nil, mapErr
		}
		side := domain.OrderSideBuy
		if v.Direction == pb.TradeDirection_TRADE_DIRECTION_SELL {
			side = domain.OrderSideSell
		}
		trade := domain.MarketTrade{ID: fmt.Sprintf("%s:%d:%d:%s", id, v.Time.AsTime().UnixNano(), v.Quantity, v.Direction.String()),
			Price: p, Quantity: domain.Quantity{Value: decimal.NewFromInt(v.Quantity)}, Side: side}
		event = &domain.MarketEvent{InstrumentID: id, Kind: domain.MarketEventTrade, ExchangeTime: utc(v.Time.AsTime()), Trade: &trade}
	case resp.GetLastPrice() != nil:
		v := resp.GetLastPrice()
		id := domain.InstrumentID(v.InstrumentUid)
		p, mapErr := price(v.Price, assets[id])
		if mapErr != nil {
			return nil, mapErr
		}
		event = &domain.MarketEvent{InstrumentID: id, Kind: domain.MarketEventLastPrice, ExchangeTime: utc(v.Time.AsTime()), LastPrice: &p}
	case resp.GetTradingStatus() != nil:
		v := resp.GetTradingStatus()
		status := mapStatus(v.TradingStatus)
		event = &domain.MarketEvent{InstrumentID: domain.InstrumentID(v.InstrumentUid), Kind: domain.MarketEventTradingStatus, ExchangeTime: utc(v.Time.AsTime()), TradingStatus: &status}
	default:
		return nil, nil
	}
	return event, nil
}

func streamCandleInterval(interval pb.SubscriptionInterval) (time.Duration, error) {
	switch interval {
	case pb.SubscriptionInterval_SUBSCRIPTION_INTERVAL_ONE_MINUTE:
		return time.Minute, nil
	case pb.SubscriptionInterval_SUBSCRIPTION_INTERVAL_FIVE_MINUTES:
		return 5 * time.Minute, nil
	case pb.SubscriptionInterval_SUBSCRIPTION_INTERVAL_FIFTEEN_MINUTES:
		return 15 * time.Minute, nil
	case pb.SubscriptionInterval_SUBSCRIPTION_INTERVAL_ONE_HOUR:
		return time.Hour, nil
	default:
		return 0, fmt.Errorf("unsupported stream candle interval %s", interval)
	}
}

func backoff(attempt uint) time.Duration {
	d := 250 * time.Millisecond
	for i := uint(0); i < attempt && d < 30*time.Second; i++ {
		d *= 2
	}
	if d > 30*time.Second {
		return 30 * time.Second
	}
	return d
}
func subscriptionInterval(d time.Duration) (pb.SubscriptionInterval, error) {
	switch d {
	case time.Minute:
		return pb.SubscriptionInterval_SUBSCRIPTION_INTERVAL_ONE_MINUTE, nil
	case 5 * time.Minute:
		return pb.SubscriptionInterval_SUBSCRIPTION_INTERVAL_FIVE_MINUTES, nil
	case 15 * time.Minute:
		return pb.SubscriptionInterval_SUBSCRIPTION_INTERVAL_FIFTEEN_MINUTES, nil
	case time.Hour:
		return pb.SubscriptionInterval_SUBSCRIPTION_INTERVAL_ONE_HOUR, nil
	default:
		return 0, errors.New("unsupported streaming candle interval")
	}
}
func ptr[T any](v T) *T { return &v }
