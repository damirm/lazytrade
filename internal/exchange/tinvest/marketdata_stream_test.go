package tinvest

import (
	"context"
	"errors"
	"io"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/damirm/lazytrade/internal/domain"
	"github.com/damirm/lazytrade/internal/exchange"
	"google.golang.org/grpc"
	"google.golang.org/protobuf/types/known/timestamppb"
	pb "opensource.tbank.ru/invest/invest-go/proto"
)

type marketReceive struct {
	response *pb.MarketDataResponse
	err      error
}

type marketStreamStub struct {
	pb.MarketDataStreamService_MarketDataStreamClient
	ctx       context.Context
	sendErr   error
	sends     atomic.Int32
	receives  chan marketReceive
	delivered chan struct{}
}

func (s *marketStreamStub) Send(*pb.MarketDataRequest) error {
	s.sends.Add(1)
	return s.sendErr
}

func (s *marketStreamStub) Recv() (*pb.MarketDataResponse, error) {
	select {
	case received := <-s.receives:
		if s.delivered != nil {
			s.delivered <- struct{}{}
		}
		return received.response, received.err
	case <-s.ctx.Done():
		return nil, s.ctx.Err()
	}
}

type marketOpenerStub struct {
	pb.MarketDataStreamServiceClient
	stream  *marketStreamStub
	openErr error
	opens   atomic.Int32
	ctx     context.Context
}

type instrumentLookupStub struct{ pb.InstrumentsServiceClient }

func (instrumentLookupStub) GetInstrumentBy(context.Context, *pb.InstrumentRequest, ...grpc.CallOption) (*pb.InstrumentResponse, error) {
	return nil, errors.New("instrument lookup failed")
}

func (o *marketOpenerStub) MarketDataStream(ctx context.Context, _ ...grpc.CallOption) (pb.MarketDataStreamService_MarketDataStreamClient, error) {
	o.opens.Add(1)
	o.ctx = ctx
	if o.openErr != nil {
		return nil, o.openErr
	}
	o.stream.ctx = ctx
	return o.stream, nil
}

func marketTestAdapter(opener *marketOpenerStub) *Adapter {
	a := orderTestAdapter(&sandboxStub{})
	a.marketStream = opener
	return a
}

func marketSubscription() []exchange.Subscription {
	return []exchange.Subscription{{Kind: exchange.SubscriptionLastPrice, InstrumentID: "instrument"}}
}

func awaitClosed[T any](t *testing.T, ch <-chan T) {
	t.Helper()
	select {
	case value, ok := <-ch:
		if ok {
			t.Fatalf("channel yielded unexpected value: %v", value)
		}
	case <-time.After(time.Second):
		t.Fatal("channel did not close")
	}
}

func awaitTerminalError(t *testing.T, ch <-chan error, contains string) {
	t.Helper()
	select {
	case err, ok := <-ch:
		if !ok || err == nil || !strings.Contains(err.Error(), contains) {
			t.Fatalf("terminal error = %v, open = %t; want %q", err, ok, contains)
		}
	case <-time.After(time.Second):
		t.Fatal("terminal error not delivered")
	}
	awaitClosed(t, ch)
}

func TestMarketStreamSetupFailuresAreSynchronousAndCancelChild(t *testing.T) {
	for _, test := range []struct {
		name     string
		openErr  error
		sendErr  error
		wantSend int32
	}{
		{name: "open", openErr: errors.New("open failed")},
		{name: "send", sendErr: errors.New("send failed"), wantSend: 1},
	} {
		t.Run(test.name, func(t *testing.T) {
			opener := &marketOpenerStub{openErr: test.openErr, stream: &marketStreamStub{sendErr: test.sendErr}}
			_, err := marketTestAdapter(opener).SubscribeMarketData(context.Background(), marketSubscription())
			if err == nil || !strings.Contains(err.Error(), "failed") {
				t.Fatalf("setup error = %v", err)
			}
			if got := opener.opens.Load(); got != 1 {
				t.Fatalf("open count = %d", got)
			}
			if got := opener.stream.sends.Load(); got != test.wantSend {
				t.Fatalf("send count = %d", got)
			}
			select {
			case <-opener.ctx.Done():
			default:
				t.Fatal("child context was not canceled after failed setup")
			}
		})
	}
}

func TestMarketStreamValidationAndMetadataFailurePrecedeOpen(t *testing.T) {
	opener := &marketOpenerStub{stream: &marketStreamStub{}}
	adapter := marketTestAdapter(opener)
	if _, err := adapter.SubscribeMarketData(context.Background(), []exchange.Subscription{{Kind: exchange.SubscriptionCandles, InstrumentID: "instrument"}}); err == nil {
		t.Fatal("invalid subscription was accepted")
	}
	adapter.instruments = instrumentLookupStub{}
	if _, err := adapter.SubscribeMarketData(context.Background(), []exchange.Subscription{{Kind: exchange.SubscriptionLastPrice, InstrumentID: "missing"}}); err == nil || !strings.Contains(err.Error(), "instrument lookup failed") {
		t.Fatalf("metadata error = %v", err)
	}
	if got := opener.opens.Load(); got != 0 {
		t.Fatalf("stream opened %d times before setup completed", got)
	}
}

func TestMarketStreamTerminalReceiveErrorIsDeliveredOnceWithoutReopen(t *testing.T) {
	receives := make(chan marketReceive, 1)
	receives <- marketReceive{err: errors.New("receive failed")}
	opener := &marketOpenerStub{stream: &marketStreamStub{receives: receives}}
	stream, err := marketTestAdapter(opener).SubscribeMarketData(context.Background(), marketSubscription())
	if err != nil {
		t.Fatal(err)
	}
	awaitTerminalError(t, stream.Errors, "receive failed")
	awaitClosed(t, stream.Events)
	if got := opener.opens.Load(); got != 1 {
		t.Fatalf("open count = %d", got)
	}
	if got := opener.stream.sends.Load(); got != 1 {
		t.Fatalf("subscription send count = %d", got)
	}
	select {
	case <-opener.ctx.Done():
	default:
		t.Fatal("child context was not canceled on receive failure")
	}
}

func TestMarketStreamEOFIsTerminal(t *testing.T) {
	receives := make(chan marketReceive, 1)
	receives <- marketReceive{err: io.EOF}
	opener := &marketOpenerStub{stream: &marketStreamStub{receives: receives}}
	stream, err := marketTestAdapter(opener).SubscribeMarketData(context.Background(), marketSubscription())
	if err != nil {
		t.Fatal(err)
	}
	awaitTerminalError(t, stream.Errors, "EOF")
	awaitClosed(t, stream.Events)
}

func TestMarketStreamCancellationQuietlyClosesChannels(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	opener := &marketOpenerStub{stream: &marketStreamStub{receives: make(chan marketReceive)}}
	stream, err := marketTestAdapter(opener).SubscribeMarketData(ctx, marketSubscription())
	if err != nil {
		t.Fatal(err)
	}
	cancel()
	awaitClosed(t, stream.Events)
	awaitClosed(t, stream.Errors)
}

func TestMarketStreamCancellationUnblocksFullEventBuffer(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	receives := make(chan marketReceive, 129)
	for range 129 {
		receives <- marketReceive{response: &pb.MarketDataResponse{Payload: &pb.MarketDataResponse_LastPrice{LastPrice: &pb.LastPrice{
			InstrumentUid: "instrument", Time: timestamppb.Now(), Price: &pb.Quotation{Units: 10},
		}}}}
	}
	delivered := make(chan struct{}, 129)
	opener := &marketOpenerStub{stream: &marketStreamStub{receives: receives, delivered: delivered}}
	stream, err := marketTestAdapter(opener).SubscribeMarketData(ctx, marketSubscription())
	if err != nil {
		t.Fatal(err)
	}
	for range 129 {
		select {
		case <-delivered:
		case <-time.After(time.Second):
			t.Fatal("receiver did not read enough events to fill event buffer")
		}
	}
	cancel()
	// The receiver must terminate while the event queue remains full. Draining
	// Events first could free a slot and hide a missing cancellation branch.
	awaitClosed(t, stream.Errors)
	for {
		select {
		case _, ok := <-stream.Events:
			if !ok {
				return
			}
		case <-time.After(time.Second):
			t.Fatal("full event buffer blocked stream shutdown")
		}
	}
}

func TestMarketStreamDeliversMappedEvent(t *testing.T) {
	at := time.Date(2026, 9, 22, 10, 0, 0, 0, time.UTC)
	receives := make(chan marketReceive, 1)
	receives <- marketReceive{response: &pb.MarketDataResponse{Payload: &pb.MarketDataResponse_LastPrice{LastPrice: &pb.LastPrice{
		InstrumentUid: "instrument", Time: timestamppb.New(at), Price: &pb.Quotation{Units: 10},
	}}}}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	opener := &marketOpenerStub{stream: &marketStreamStub{receives: receives}}
	stream, err := marketTestAdapter(opener).SubscribeMarketData(ctx, marketSubscription())
	if err != nil {
		t.Fatal(err)
	}
	select {
	case event := <-stream.Events:
		if event.ExchangeAccountID != domain.ExchangeAccountID("sandbox-account") ||
			event.InstrumentID != "instrument" || event.Kind != domain.MarketEventLastPrice ||
			event.LastPrice == nil || event.LastPrice.Asset != "RUB" || !event.ExchangeTime.Equal(at) ||
			event.Sequence != 1 || event.ReceivedTime.IsZero() {
			t.Fatalf("mapped event = %+v", event)
		}
	case <-time.After(time.Second):
		t.Fatal("mapped event not delivered")
	}
	cancel()
	awaitClosed(t, stream.Events)
	awaitClosed(t, stream.Errors)
}
