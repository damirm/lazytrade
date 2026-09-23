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

func (a *Adapter) Candles(ctx context.Context, q exchange.CandleQuery) ([]domain.Candle, error) {
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
	if resp == nil {
		return nil, errors.New("candles response is missing")
	}
	result := make([]domain.Candle, 0, len(resp.Candles))
	for _, c := range resp.Candles {
		if c == nil {
			return nil, errors.New("candle is missing")
		}
		start, err := requiredTime(c.Time)
		if err != nil {
			return nil, fmt.Errorf("candle time: %w", err)
		}
		item := domain.Candle{Start: start, End: start.Add(q.Interval), Interval: q.Interval,
			Volume: domain.Quantity{Value: decimal.NewFromInt(c.Volume)}, Complete: c.IsComplete}
		if err := mapOHLC(&item, q.Asset, c.Open, c.High, c.Low, c.Close); err != nil {
			return nil, err
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
	if resp == nil {
		return nil, errors.New("last prices response is missing")
	}
	result := make(map[domain.InstrumentID]domain.Price, len(resp.LastPrices))
	for _, item := range resp.LastPrices {
		if item == nil {
			return nil, errors.New("last price is missing")
		}
		if err := domain.InstrumentID(item.InstrumentUid).Validate(); err != nil {
			return nil, fmt.Errorf("last price instrument: %w", err)
		}
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
	if resp == nil {
		return domain.OrderBook{}, errors.New("order book response is missing")
	}
	result := domain.OrderBook{Depth: depth}
	for _, side := range []struct {
		src []*pb.Order
		dst *[]domain.OrderBookLevel
	}{{resp.Bids, &result.Bids}, {resp.Asks, &result.Asks}} {
		for _, level := range side.src {
			if level == nil {
				return domain.OrderBook{}, errors.New("order book level is missing")
			}
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
	if resp == nil {
		return nil, errors.New("last trades response is missing")
	}
	result := make([]domain.MarketTrade, 0, len(resp.Trades))
	for _, item := range resp.Trades {
		trade, _, err := mapTrade(item, asset)
		if err != nil {
			return nil, err
		}
		result = append(result, trade)
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
	if resp == nil {
		return 0, errors.New("trading status response is missing")
	}
	return mapStatus(resp.TradingStatus)
}

func (a *Adapter) SubscribeMarketData(ctx context.Context, subscriptions []exchange.Subscription) (exchange.MarketStream, error) {
	for i, s := range subscriptions {
		if err := s.Validate(); err != nil {
			return exchange.MarketStream{}, fmt.Errorf("subscription %d: %w", i, err)
		}
	}
	streamCtx, cancel := context.WithCancel(ctx)
	assets, err := a.subscriptionAssets(streamCtx, subscriptions)
	if err != nil {
		cancel()
		return exchange.MarketStream{}, fmt.Errorf("resolve subscription assets: %w", err)
	}
	stream, err := a.marketStream.MarketDataStream(streamCtx)
	if err != nil {
		cancel()
		return exchange.MarketStream{}, mapError("open market data stream", err)
	}
	if err := sendSubscriptions(stream, subscriptions); err != nil {
		cancel()
		return exchange.MarketStream{}, mapError("subscribe market data", err)
	}
	events := make(chan domain.MarketEvent, 128)
	errs := make(chan error, 1)
	go func() {
		defer close(events)
		defer close(errs)
		defer cancel()
		if err := a.receive(streamCtx, stream, assets, events); err != nil && ctx.Err() == nil {
			errs <- mapError("receive market data", err)
		}
	}()
	return exchange.MarketStream{Events: events, Errors: errs}, nil
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
		if err := event.Validate(); err != nil {
			return fmt.Errorf("market event: %w", err)
		}
		select {
		case events <- *event:
		case <-ctx.Done():
			return ctx.Err()
		}
	}
}

func mapStreamResponse(resp *pb.MarketDataResponse, assets map[domain.InstrumentID]string) (*domain.MarketEvent, error) {
	if resp == nil {
		return nil, errors.New("market data response is missing")
	}
	var event *domain.MarketEvent
	switch {
	case resp.GetCandle() != nil:
		v := resp.GetCandle()
		id := domain.InstrumentID(v.InstrumentUid)
		interval, mapErr := streamCandleInterval(v.Interval)
		if mapErr != nil {
			return nil, mapErr
		}
		start, mapErr := requiredTime(v.Time)
		if mapErr != nil {
			return nil, fmt.Errorf("candle time: %w", mapErr)
		}
		candle := domain.Candle{Start: start, End: start.Add(interval), Interval: interval,
			Volume: domain.Quantity{Value: decimal.NewFromInt(v.Volume)}, Complete: true}
		if err := mapOHLC(&candle, assets[id], v.Open, v.High, v.Low, v.Close); err != nil {
			return nil, err
		}
		if err := candle.Validate(); err != nil {
			return nil, err
		}
		// A candle-close event becomes observable at the end of its interval.
		// Using the start here would make live strategy cursors and trading-day
		// attribution differ from backtests.
		event = &domain.MarketEvent{InstrumentID: id, Kind: domain.MarketEventCandleClose, ExchangeTime: candle.End, Candle: &candle}
	case resp.GetOrderbook() != nil:
		v := resp.GetOrderbook()
		id := domain.InstrumentID(v.InstrumentUid)
		at, err := requiredTime(v.Time)
		if err != nil {
			return nil, fmt.Errorf("order book time: %w", err)
		}
		book := domain.OrderBook{Depth: int(v.Depth)}
		for _, side := range []struct {
			src []*pb.Order
			dst *[]domain.OrderBookLevel
		}{{v.Bids, &book.Bids}, {v.Asks, &book.Asks}} {
			for _, level := range side.src {
				if level == nil {
					return nil, errors.New("order book level is missing")
				}
				p, mapErr := price(level.Price, assets[id])
				if mapErr != nil {
					return nil, mapErr
				}
				*side.dst = append(*side.dst, domain.OrderBookLevel{Price: p, Quantity: domain.Quantity{Value: decimal.NewFromInt(level.Quantity)}})
			}
		}
		if err := book.Validate(); err != nil {
			return nil, err
		}
		event = &domain.MarketEvent{InstrumentID: id, Kind: domain.MarketEventOrderBook, ExchangeTime: at, OrderBook: &book}
	case resp.GetTrade() != nil:
		v := resp.GetTrade()
		id := domain.InstrumentID(v.InstrumentUid)
		trade, at, err := mapTrade(v, assets[id])
		if err != nil {
			return nil, err
		}
		event = &domain.MarketEvent{InstrumentID: id, Kind: domain.MarketEventTrade, ExchangeTime: at, Trade: &trade}
	case resp.GetLastPrice() != nil:
		v := resp.GetLastPrice()
		id := domain.InstrumentID(v.InstrumentUid)
		at, err := requiredTime(v.Time)
		if err != nil {
			return nil, fmt.Errorf("last price time: %w", err)
		}
		p, mapErr := price(v.Price, assets[id])
		if mapErr != nil {
			return nil, mapErr
		}
		event = &domain.MarketEvent{InstrumentID: id, Kind: domain.MarketEventLastPrice, ExchangeTime: at, LastPrice: &p}
	case resp.GetTradingStatus() != nil:
		v := resp.GetTradingStatus()
		status, err := mapStatus(v.TradingStatus)
		if err != nil {
			return nil, err
		}
		at, err := requiredTime(v.Time)
		if err != nil {
			return nil, fmt.Errorf("trading status time: %w", err)
		}
		event = &domain.MarketEvent{InstrumentID: domain.InstrumentID(v.InstrumentUid), Kind: domain.MarketEventTradingStatus, ExchangeTime: at, TradingStatus: &status}
	default:
		switch resp.Payload.(type) {
		case nil, *pb.MarketDataResponse_Candle, *pb.MarketDataResponse_Orderbook,
			*pb.MarketDataResponse_Trade, *pb.MarketDataResponse_LastPrice, *pb.MarketDataResponse_TradingStatus:
			return nil, errors.New("market data payload is missing")
		}
		// Subscription acknowledgements and pings do not carry market events.
		return nil, nil
	}
	if err := event.InstrumentID.Validate(); err != nil {
		return nil, fmt.Errorf("market event instrument: %w", err)
	}
	return event, nil
}

func mapTrade(value *pb.Trade, asset string) (domain.MarketTrade, time.Time, error) {
	if value == nil {
		return domain.MarketTrade{}, time.Time{}, errors.New("trade is missing")
	}
	if err := domain.InstrumentID(value.InstrumentUid).Validate(); err != nil {
		return domain.MarketTrade{}, time.Time{}, fmt.Errorf("trade instrument: %w", err)
	}
	at, err := requiredTime(value.Time)
	if err != nil {
		return domain.MarketTrade{}, time.Time{}, fmt.Errorf("trade time: %w", err)
	}
	side, err := tradeSide(value.Direction)
	if err != nil {
		return domain.MarketTrade{}, time.Time{}, err
	}
	p, err := price(value.Price, asset)
	if err != nil {
		return domain.MarketTrade{}, time.Time{}, err
	}
	trade := domain.MarketTrade{
		ID:    fmt.Sprintf("%s:%d:%d:%s", value.InstrumentUid, at.UnixNano(), value.Quantity, value.Direction.String()),
		Price: p, Quantity: domain.Quantity{Value: decimal.NewFromInt(value.Quantity)}, Side: side,
	}
	return trade, at, trade.Validate()
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
