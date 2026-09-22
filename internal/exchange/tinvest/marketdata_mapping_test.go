package tinvest

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/damirm/lazytrade/internal/domain"
	"google.golang.org/grpc"
	"google.golang.org/protobuf/types/known/timestamppb"
	pb "opensource.tbank.ru/invest/invest-go/proto"
)

type marketDataStub struct {
	pb.MarketDataServiceClient
	candles *pb.GetCandlesResponse
	trades  *pb.GetLastTradesResponse
	prices  *pb.GetLastPricesResponse
	book    *pb.GetOrderBookResponse
	status  *pb.GetTradingStatusResponse
}

func (s marketDataStub) GetLastPrices(context.Context, *pb.GetLastPricesRequest, ...grpc.CallOption) (*pb.GetLastPricesResponse, error) {
	return s.prices, nil
}
func (s marketDataStub) GetOrderBook(context.Context, *pb.GetOrderBookRequest, ...grpc.CallOption) (*pb.GetOrderBookResponse, error) {
	return s.book, nil
}
func (s marketDataStub) GetTradingStatus(context.Context, *pb.GetTradingStatusRequest, ...grpc.CallOption) (*pb.GetTradingStatusResponse, error) {
	return s.status, nil
}

func (s marketDataStub) GetCandles(context.Context, *pb.GetCandlesRequest, ...grpc.CallOption) (*pb.GetCandlesResponse, error) {
	return s.candles, nil
}

func (s marketDataStub) GetLastTrades(context.Context, *pb.GetLastTradesRequest, ...grpc.CallOption) (*pb.GetLastTradesResponse, error) {
	return s.trades, nil
}

func TestCandleMappingPreservesAliasedOHLC(t *testing.T) {
	q := &pb.Quotation{Units: 10}
	at := timestamppb.Now()
	adapter := &Adapter{market: marketDataStub{candles: &pb.GetCandlesResponse{Candles: []*pb.HistoricCandle{{
		Time: at, Open: q, High: q, Low: q, Close: q, IsComplete: true,
	}}}}, timeout: time.Second}
	candles, err := adapter.Candles(context.Background(), CandleQuery{InstrumentID: "instrument", Asset: "RUB", Interval: time.Minute})
	if err != nil || len(candles) != 1 {
		t.Fatalf("candles = %v, error = %v", candles, err)
	}
	response := &pb.MarketDataResponse{Payload: &pb.MarketDataResponse_Candle{Candle: &pb.Candle{
		InstrumentUid: "instrument", Time: at, Interval: pb.SubscriptionInterval_SUBSCRIPTION_INTERVAL_ONE_MINUTE,
		Open: q, High: q, Low: q, Close: q,
	}}}
	event, err := mapStreamResponse(response, map[domain.InstrumentID]string{"instrument": "RUB"})
	if err != nil || event == nil {
		t.Fatalf("event = %v, error = %v", event, err)
	}
	for _, candle := range []domain.Candle{candles[0], *event.Candle} {
		for _, p := range []domain.Price{candle.Open, candle.High, candle.Low, candle.Close} {
			if p.Value.String() != "10" || p.Asset != "RUB" {
				t.Fatalf("OHLC field = %+v", p)
			}
		}
	}
}

func TestMarketMappingRejectsUnknownEnums(t *testing.T) {
	for _, value := range []int32{0, -1, 999} {
		t.Run("trade/"+pb.TradeDirection(value).String(), func(t *testing.T) {
			trade := &pb.Trade{InstrumentUid: "instrument", Time: timestamppb.Now(), Price: &pb.Quotation{Units: 10}, Quantity: 1, Direction: pb.TradeDirection(value)}
			_, err := mapStreamResponse(&pb.MarketDataResponse{Payload: &pb.MarketDataResponse_Trade{Trade: trade}}, map[domain.InstrumentID]string{"instrument": "RUB"})
			if err == nil {
				t.Fatal("stream accepted unknown direction")
			}
			adapter := &Adapter{market: marketDataStub{trades: &pb.GetLastTradesResponse{Trades: []*pb.Trade{trade}}}, timeout: time.Second}
			if _, err := adapter.LastTrades(context.Background(), "instrument", "RUB", time.Now(), time.Now()); err == nil {
				t.Fatal("history accepted unknown direction")
			}
		})
		t.Run("status/"+pb.SecurityTradingStatus(value).String(), func(t *testing.T) {
			_, err := mapStreamResponse(&pb.MarketDataResponse{Payload: &pb.MarketDataResponse_TradingStatus{TradingStatus: &pb.TradingStatus{
				InstrumentUid: "instrument", Time: timestamppb.Now(), TradingStatus: pb.SecurityTradingStatus(value),
			}}}, nil)
			if err == nil {
				t.Fatal("accepted unknown trading status")
			}
		})
	}
}

func TestMarketStreamRejectsMissingOrInvalidTime(t *testing.T) {
	for _, at := range []*timestamppb.Timestamp{nil, {Nanos: -1}, {Seconds: 253402300800}, timestamppb.New(time.Time{})} {
		q := &pb.Quotation{Units: 10}
		responses := []*pb.MarketDataResponse{
			{Payload: &pb.MarketDataResponse_Candle{Candle: &pb.Candle{InstrumentUid: "instrument", Time: at, Interval: pb.SubscriptionInterval_SUBSCRIPTION_INTERVAL_ONE_MINUTE, Open: q, High: q, Low: q, Close: q}}},
			{Payload: &pb.MarketDataResponse_Orderbook{Orderbook: &pb.OrderBook{InstrumentUid: "instrument", Time: at, Depth: 1}}},
			{Payload: &pb.MarketDataResponse_Trade{Trade: &pb.Trade{InstrumentUid: "instrument", Time: at, Price: q, Quantity: 1, Direction: pb.TradeDirection_TRADE_DIRECTION_BUY}}},
			{Payload: &pb.MarketDataResponse_LastPrice{LastPrice: &pb.LastPrice{InstrumentUid: "instrument", Time: at, Price: q}}},
			{Payload: &pb.MarketDataResponse_TradingStatus{TradingStatus: &pb.TradingStatus{InstrumentUid: "instrument", Time: at, TradingStatus: pb.SecurityTradingStatus_SECURITY_TRADING_STATUS_NORMAL_TRADING}}},
		}
		for _, response := range responses {
			if _, err := mapStreamResponse(response, map[domain.InstrumentID]string{"instrument": "RUB"}); err == nil {
				t.Errorf("accepted %T with time %v", response.Payload, at)
			}
		}
	}
}

func TestCandleMappingReportsFirstMissingPrice(t *testing.T) {
	for i := 0; i < 100; i++ {
		response := &pb.MarketDataResponse{Payload: &pb.MarketDataResponse_Candle{Candle: &pb.Candle{
			InstrumentUid: "instrument", Time: timestamppb.Now(), Interval: pb.SubscriptionInterval_SUBSCRIPTION_INTERVAL_ONE_MINUTE,
		}}}
		if _, err := mapStreamResponse(response, map[domain.InstrumentID]string{"instrument": "RUB"}); err == nil || !strings.Contains(err.Error(), "open price") {
			t.Fatalf("first error = %v, want open price", err)
		}
	}
}

func TestMarketUnaryRejectsMissingData(t *testing.T) {
	ctx := context.Background()
	for _, stub := range []marketDataStub{
		{},
		{candles: &pb.GetCandlesResponse{Candles: []*pb.HistoricCandle{nil}}, trades: &pb.GetLastTradesResponse{Trades: []*pb.Trade{nil}}, prices: &pb.GetLastPricesResponse{LastPrices: []*pb.LastPrice{nil}}, book: &pb.GetOrderBookResponse{Bids: []*pb.Order{nil}}, status: &pb.GetTradingStatusResponse{}},
		{candles: &pb.GetCandlesResponse{Candles: []*pb.HistoricCandle{{Time: timestamppb.Now()}}}, trades: &pb.GetLastTradesResponse{Trades: []*pb.Trade{{InstrumentUid: "instrument", Time: timestamppb.Now(), Direction: pb.TradeDirection_TRADE_DIRECTION_BUY, Quantity: 1}}}, prices: &pb.GetLastPricesResponse{LastPrices: []*pb.LastPrice{{InstrumentUid: "instrument"}}}, book: &pb.GetOrderBookResponse{Bids: []*pb.Order{{Quantity: 1}}}, status: &pb.GetTradingStatusResponse{TradingStatus: 999}},
	} {
		a := &Adapter{market: stub, timeout: time.Second}
		if _, err := a.Candles(ctx, CandleQuery{InstrumentID: "instrument", Asset: "RUB", Interval: time.Minute}); err == nil {
			t.Error("accepted incomplete candles")
		}
		if _, err := a.LastTrades(ctx, "instrument", "RUB", time.Now(), time.Now()); err == nil {
			t.Error("accepted incomplete trades")
		}
		if _, err := a.LastPrices(ctx, []domain.InstrumentID{"instrument"}, "RUB"); err == nil {
			t.Error("accepted incomplete last prices")
		}
		if _, err := a.OrderBook(ctx, "instrument", "RUB", 1); err == nil {
			t.Error("accepted incomplete order book")
		}
		if _, err := a.TradingStatus(ctx, "instrument"); err == nil {
			t.Error("accepted incomplete trading status")
		}
	}
}

func TestMarketUnaryRejectsMissingOrInvalidTime(t *testing.T) {
	for _, at := range []*timestamppb.Timestamp{nil, {Nanos: 1_000_000_000}, {Seconds: 253402300800}, timestamppb.New(time.Time{})} {
		q := &pb.Quotation{Units: 10}
		a := &Adapter{timeout: time.Second, market: marketDataStub{
			candles: &pb.GetCandlesResponse{Candles: []*pb.HistoricCandle{{Time: at, Open: q, High: q, Low: q, Close: q}}},
			trades:  &pb.GetLastTradesResponse{Trades: []*pb.Trade{{InstrumentUid: "instrument", Time: at, Price: q, Quantity: 1, Direction: pb.TradeDirection_TRADE_DIRECTION_BUY}}},
		}}
		if _, err := a.Candles(context.Background(), CandleQuery{InstrumentID: "instrument", Asset: "RUB", Interval: time.Minute}); err == nil {
			t.Errorf("accepted candle time %v", at)
		}
		if _, err := a.LastTrades(context.Background(), "instrument", "RUB", time.Now(), time.Now()); err == nil {
			t.Errorf("accepted trade time %v", at)
		}
	}
}

func TestMarketStreamMappingFailureCancelsRPC(t *testing.T) {
	receives := make(chan marketReceive, 1)
	receives <- marketReceive{response: &pb.MarketDataResponse{Payload: &pb.MarketDataResponse_LastPrice{LastPrice: &pb.LastPrice{InstrumentUid: "instrument", Price: &pb.Quotation{Units: 10}}}}}
	opener := &marketOpenerStub{stream: &marketStreamStub{receives: receives}}
	stream, err := marketTestAdapter(opener).SubscribeMarketData(context.Background(), marketSubscription())
	if err != nil {
		t.Fatal(err)
	}
	awaitTerminalError(t, stream.Errors, "timestamp")
	awaitClosed(t, stream.Events)
	select {
	case <-opener.ctx.Done():
	default:
		t.Fatal("mapping failure did not cancel RPC")
	}
	if opener.opens.Load() != 1 {
		t.Fatal("mapping failure reopened stream")
	}
}

func TestMarketMappingRejectsMissingPayload(t *testing.T) {
	for _, response := range []*pb.MarketDataResponse{
		nil, {},
		{Payload: &pb.MarketDataResponse_Candle{}},
		{Payload: &pb.MarketDataResponse_Orderbook{}},
		{Payload: &pb.MarketDataResponse_Trade{}},
		{Payload: &pb.MarketDataResponse_LastPrice{}},
		{Payload: &pb.MarketDataResponse_TradingStatus{}},
	} {
		if _, err := mapStreamResponse(response, nil); err == nil {
			t.Errorf("accepted missing payload: %v", response)
		}
	}
	for _, response := range []*pb.MarketDataResponse{
		{Payload: &pb.MarketDataResponse_Ping{Ping: &pb.Ping{}}},
		{Payload: &pb.MarketDataResponse_SubscribeCandlesResponse{SubscribeCandlesResponse: &pb.SubscribeCandlesResponse{}}},
	} {
		if event, err := mapStreamResponse(response, nil); err != nil || event != nil {
			t.Errorf("control message produced event %v, error %v", event, err)
		}
	}
}
