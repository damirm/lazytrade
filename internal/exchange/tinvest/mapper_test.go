package tinvest

import (
	"errors"
	"sync"
	"testing"
	"time"

	"github.com/damirm/lazytrade/internal/domain"
	"github.com/damirm/lazytrade/internal/exchange"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
	"google.golang.org/protobuf/types/known/timestamppb"
	pb "opensource.tbank.ru/invest/invest-go/proto"
)

func TestQuotationExact(t *testing.T) {
	tests := []struct {
		name string
		q    *pb.Quotation
		want string
	}{
		{"positive", &pb.Quotation{Units: 12, Nano: 345678901}, "12.345678901"},
		{"negative fraction", &pb.Quotation{Units: 0, Nano: -1}, "-0.000000001"},
		{"negative", &pb.Quotation{Units: -12, Nano: -345678901}, "-12.345678901"},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			got, err := quotation(test.q)
			if err != nil {
				t.Fatal(err)
			}
			if got.String() != test.want {
				t.Fatalf("got %s, want %s", got, test.want)
			}
		})
	}
}

func TestMapEveryStreamPayload(t *testing.T) {
	id := domain.InstrumentID("uid-1")
	at := timestamppb.New(time.Date(2026, 1, 2, 3, 4, 5, 0, time.UTC))
	q := func(units int64) *pb.Quotation { return &pb.Quotation{Units: units} }
	tests := []struct {
		name string
		resp *pb.MarketDataResponse
		kind domain.MarketEventKind
	}{
		{"candle", &pb.MarketDataResponse{Payload: &pb.MarketDataResponse_Candle{Candle: &pb.Candle{
			InstrumentUid: string(id), Time: at, Interval: pb.SubscriptionInterval_SUBSCRIPTION_INTERVAL_ONE_MINUTE,
			Open: q(10), High: q(12), Low: q(9), Close: q(11), Volume: 2,
		}}}, domain.MarketEventCandleClose},
		{"order book", &pb.MarketDataResponse{Payload: &pb.MarketDataResponse_Orderbook{Orderbook: &pb.OrderBook{
			InstrumentUid: string(id), Time: at, Depth: 1, Bids: []*pb.Order{{Price: q(10), Quantity: 1}}, Asks: []*pb.Order{{Price: q(11), Quantity: 2}},
		}}}, domain.MarketEventOrderBook},
		{"trade", &pb.MarketDataResponse{Payload: &pb.MarketDataResponse_Trade{Trade: &pb.Trade{
			InstrumentUid: string(id), Time: at, Price: q(10), Quantity: 1, Direction: pb.TradeDirection_TRADE_DIRECTION_BUY,
		}}}, domain.MarketEventTrade},
		{"last price", &pb.MarketDataResponse{Payload: &pb.MarketDataResponse_LastPrice{LastPrice: &pb.LastPrice{
			InstrumentUid: string(id), Time: at, Price: q(10),
		}}}, domain.MarketEventLastPrice},
		{"status", &pb.MarketDataResponse{Payload: &pb.MarketDataResponse_TradingStatus{TradingStatus: &pb.TradingStatus{
			InstrumentUid: string(id), Time: at, TradingStatus: pb.SecurityTradingStatus_SECURITY_TRADING_STATUS_NORMAL_TRADING,
		}}}, domain.MarketEventTradingStatus},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			event, err := mapStreamResponse(test.resp, map[domain.InstrumentID]string{id: "RUB"})
			if err != nil {
				t.Fatal(err)
			}
			if event == nil || event.Kind != test.kind {
				t.Fatalf("unexpected event %#v", event)
			}
			if test.kind == domain.MarketEventCandleClose && !event.Candle.Complete {
				t.Fatal("waiting_close candle must map as complete")
			}
			if test.kind == domain.MarketEventCandleClose && !event.ExchangeTime.Equal(event.Candle.End) {
				t.Fatalf("candle event time = %s, want end %s", event.ExchangeTime, event.Candle.End)
			}
		})
	}
}

func TestStreamMappingFailsOnMetadataMiss(t *testing.T) {
	resp := &pb.MarketDataResponse{Payload: &pb.MarketDataResponse_LastPrice{LastPrice: &pb.LastPrice{
		InstrumentUid: "unknown", Time: timestamppb.Now(), Price: &pb.Quotation{Units: 1},
	}}}
	if _, err := mapStreamResponse(resp, nil); err == nil {
		t.Fatal("expected missing asset error")
	}
}

func TestMetadataCacheConcurrentAccess(t *testing.T) {
	adapter := &Adapter{metadata: make(map[domain.InstrumentID]domain.Instrument)}
	const count = 50
	var wg sync.WaitGroup
	for i := 0; i < count; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			instrument := domain.Instrument{ID: "uid"}
			adapter.storeMetadata(instrument)
			if _, ok := adapter.cachedMetadata("uid"); !ok {
				t.Error("cache miss")
			}
		}()
	}
	wg.Wait()
}

func TestQuotationRejectsInvalid(t *testing.T) {
	if _, err := quotation(nil); !errors.Is(err, errMissingValue) {
		t.Fatalf("unexpected error: %v", err)
	}
	if _, err := quotation(&pb.Quotation{Nano: 1_000_000_000}); err == nil {
		t.Fatal("expected invalid nanos")
	}
}

func TestMoneyNormalizesAsset(t *testing.T) {
	got, err := money(&pb.MoneyValue{Units: 10, Nano: 5, Currency: "rub"})
	if err != nil {
		t.Fatal(err)
	}
	if got.Asset != "RUB" || got.Amount.String() != "10.000000005" {
		t.Fatalf("unexpected money: %#v", got)
	}
}

func TestMapInstrumentAllowsExchangeQualifiedTicker(t *testing.T) {
	instrument, err := mapInstrument("tinvest", &pb.Instrument{
		Uid: "uid-1", Ticker: "CIAN@US", Name: "CIAN",
		Currency: "rub", Lot: 1, MinPriceIncrement: &pb.Quotation{Nano: 10_000_000},
	})
	if err != nil {
		t.Fatal(err)
	}
	if instrument.BaseAsset != "CIAN@US" || instrument.QuoteAsset != "RUB" {
		t.Fatalf("unexpected instrument: %+v", instrument)
	}
}

func TestMapInstrumentRejectsMissingPriceIncrement(t *testing.T) {
	_, err := mapInstrument("tinvest", &pb.Instrument{
		Uid: "uid-1", Ticker: "TEST", Name: "Test", Currency: "rub", Lot: 1,
	})
	if !errors.Is(err, errMissingValue) {
		t.Fatalf("error = %v, want missing value", err)
	}
}

func TestMapError(t *testing.T) {
	tests := []struct {
		code     codes.Code
		category exchange.ErrorCategory
		retry    bool
	}{
		{codes.InvalidArgument, exchange.ErrorInvalidRequest, false},
		{codes.Unauthenticated, exchange.ErrorAuthentication, false},
		{codes.ResourceExhausted, exchange.ErrorRateLimited, true},
		{codes.Unavailable, exchange.ErrorTransient, true},
	}
	for _, test := range tests {
		err := mapError("test", status.Error(test.code, "safe"))
		var got *exchange.Error
		if !errors.As(err, &got) {
			t.Fatalf("not exchange error: %T", err)
		}
		if got.Category != test.category || got.Retryable != test.retry {
			t.Fatalf("%s: category=%v retry=%v", test.code, got.Category, got.Retryable)
		}
	}
}

func TestBackoffIsBounded(t *testing.T) {
	if got := backoff(0); got.String() != "250ms" {
		t.Fatalf("unexpected initial backoff %s", got)
	}
	if got := backoff(100); got.String() != "30s" {
		t.Fatalf("unexpected capped backoff %s", got)
	}
}
