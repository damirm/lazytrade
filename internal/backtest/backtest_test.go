package backtest

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/damirm/lazytrade/internal/clock"
	"github.com/damirm/lazytrade/internal/domain"
	"github.com/shopspring/decimal"
)

func metadata(policy GapPolicy) DatasetMetadata {
	price, _ := domain.NewPrice("0.01", "USD")
	lot, _ := domain.NewQuantity("1")
	return DatasetMetadata{
		Version: 1, ExchangeAccountID: "backtest", InstrumentID: "TEST",
		Interval: time.Minute, PriceAsset: "USD", Timezone: time.UTC,
		TimestampLayout: "2006-01-02T15:04:05", TickSize: price, LotSize: lot, GapPolicy: policy,
	}
}

func fixture(t *testing.T, name string) *os.File {
	t.Helper()
	file, err := os.Open(filepath.Join("..", "..", "testdata", "backtest", name))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = file.Close() })
	return file
}

func TestCSVValidationChecksumAndGaps(t *testing.T) {
	iterator, err := NewCSVIterator(fixture(t, "market-next-open.csv"), metadata(GapFail))
	if err != nil {
		t.Fatal(err)
	}
	count := 0
	for {
		_, err = iterator.Next(context.Background())
		if errors.Is(err, io.EOF) {
			break
		}
		if err != nil {
			t.Fatal(err)
		}
		count++
	}
	if count != 3 {
		t.Fatalf("got %d events", count)
	}
	checksum, err := iterator.Checksum()
	if err != nil || len(checksum) != 64 {
		t.Fatalf("checksum %q, err %v", checksum, err)
	}

	invalid, _ := NewCSVIterator(fixture(t, "invalid-ohlc.csv"), metadata(GapFail))
	if _, err := invalid.Next(context.Background()); err == nil {
		t.Fatal("invalid OHLC accepted")
	}
	gapped, _ := NewCSVIterator(fixture(t, "gap.csv"), metadata(GapFail))
	if _, err := gapped.Next(context.Background()); err != nil {
		t.Fatal(err)
	}
	if _, err := gapped.Next(context.Background()); err == nil {
		t.Fatal("gap accepted with fail policy")
	}
	marked, _ := NewCSVIterator(fixture(t, "gap.csv"), metadata(GapMark))
	_, _ = marked.Next(context.Background())
	if _, err := marked.Next(context.Background()); err != nil {
		t.Fatal(err)
	}
	if len(marked.Gaps()) != 1 || marked.Gaps()[0].Missing != 1 {
		t.Fatalf("unexpected gaps: %+v", marked.Gaps())
	}
}

func TestCSVIteratorCancellation(t *testing.T) {
	iterator, err := NewCSVIterator(fixture(t, "market-next-open.csv"), metadata(GapFail))
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := iterator.Next(ctx); !errors.Is(err, context.Canceled) {
		t.Fatalf("got %v, want context cancellation", err)
	}
}

func TestSimulatedBrokerNextOpenNoLookaheadAndFees(t *testing.T) {
	broker := newBroker(t, "0.03", "5")
	first := candleEvent(t, "2025-01-01T10:00:00Z", "100", "105", "99", "104")
	request := marketRequest(first.Candle.End)
	if _, err := broker.Submit(context.Background(), request); err != nil {
		t.Fatal(err)
	}
	if executions, err := broker.OnMarketEvent(context.Background(), first); err != nil || len(executions) != 0 {
		t.Fatalf("same-candle fill: %+v, %v", executions, err)
	}
	next := candleEvent(t, "2025-01-01T10:01:00Z", "110", "112", "108", "111")
	executions, err := broker.OnMarketEvent(context.Background(), next)
	if err != nil || len(executions) != 1 {
		t.Fatalf("executions %+v, err %v", executions, err)
	}
	if got := executions[0].Price.Value.String(); got != "110.06" {
		t.Fatalf("fill price %s", got)
	}
	if got := executions[0].Commission.Amount.String(); got != "0.033018" {
		t.Fatalf("commission %s", got)
	}
	if got := broker.Snapshot().SlippageCost.Amount.String(); got != "0.06" {
		t.Fatalf("slippage %s", got)
	}
}

func TestLimitTouchNoTouchAndConservativeGapImprovement(t *testing.T) {
	cases := []struct {
		name, side, limit, open, high, low, want string
		fill                                     bool
	}{
		{"buy touch", "buy", "100", "102", "103", "99", "100", true},
		{"buy no touch", "buy", "100", "102", "103", "101", "", false},
		{"buy gap improvement", "buy", "100", "95", "101", "94", "95", true},
		{"sell touch", "sell", "100", "98", "101", "97", "100", true},
		{"sell no touch", "sell", "100", "98", "99", "97", "", false},
		{"sell gap improvement", "sell", "100", "105", "106", "99", "105", true},
	}
	for index, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			broker := newBroker(t, "0", "0")
			at := time.Date(2025, 1, 1, 10, 0, 0, 0, time.UTC)
			request := marketRequest(at)
			request.OrderID = domain.OrderID(tc.name)
			request.ClientOrderID = domain.ClientOrderID(tc.name)
			request.Type = domain.OrderTypeLimit
			request.Side = domain.OrderSideBuy
			if tc.side == "sell" {
				request.Side = domain.OrderSideSell
				broker.position = decimal.NewFromInt(1)
			}
			request.LimitPrice = price(t, tc.limit)
			_, err := broker.Submit(context.Background(), request)
			if err != nil {
				t.Fatal(err)
			}
			event := candleEvent(t, at.Format(time.RFC3339), tc.open, tc.high, tc.low, tc.open)
			event.Sequence = uint64(index + 1)
			fills, err := broker.OnMarketEvent(context.Background(), event)
			if err != nil {
				t.Fatal(err)
			}
			if (len(fills) == 1) != tc.fill {
				t.Fatalf("fills %+v", fills)
			}
			if tc.fill && fills[0].Price.Value.String() != tc.want {
				t.Fatalf("price %s, want %s", fills[0].Price.Value, tc.want)
			}
		})
	}
}

func TestSimulatedBrokerEnforcesInstrumentConstraints(t *testing.T) {
	t.Parallel()
	broker := newBroker(t, "0", "0")
	at := time.Date(2025, 1, 1, 10, 0, 0, 0, time.UTC)

	unalignedQuantity := marketRequest(at)
	unalignedQuantity.Quantity = quantity("1.5")
	broker.config.LotSize = quantity("1")
	if _, err := broker.Submit(context.Background(), unalignedQuantity); !errors.Is(err, ErrQuantityNotAligned) {
		t.Fatalf("quantity error = %v", err)
	}

	unalignedPrice := marketRequest(at)
	unalignedPrice.Type = domain.OrderTypeLimit
	unalignedPrice.LimitPrice = price(t, "100.001")
	if _, err := broker.Submit(context.Background(), unalignedPrice); !errors.Is(err, ErrPriceNotAligned) {
		t.Fatalf("price error = %v", err)
	}
}

func TestSimulatedBrokerRejectsUnaffordableBuyAndNakedSell(t *testing.T) {
	t.Parallel()
	at := time.Date(2025, 1, 1, 10, 0, 0, 0, time.UTC)
	event := candleEvent(t, at.Format(time.RFC3339), "100", "101", "99", "100")

	buy := newBroker(t, "0", "0")
	buy.cash = decimal.NewFromInt(50)
	if _, err := buy.Submit(context.Background(), marketRequest(at)); err != nil {
		t.Fatal(err)
	}
	if fills, err := buy.OnMarketEvent(context.Background(), event); err != nil || len(fills) != 0 {
		t.Fatalf("buy fills = %#v, error = %v", fills, err)
	}
	if orders := buy.Orders(); len(orders) != 1 || orders[0].Status != domain.OrderStatusRejected {
		t.Fatalf("buy orders = %#v", orders)
	}
	if !buy.Snapshot().Cash.Amount.Equal(decimal.NewFromInt(50)) || !buy.Snapshot().PositionQuantity.IsZero() {
		t.Fatalf("buy snapshot = %#v", buy.Snapshot())
	}

	sell := newBroker(t, "0", "0")
	request := marketRequest(at)
	request.Side = domain.OrderSideSell
	if _, err := sell.Submit(context.Background(), request); err != nil {
		t.Fatal(err)
	}
	if fills, err := sell.OnMarketEvent(context.Background(), event); err != nil || len(fills) != 0 {
		t.Fatalf("sell fills = %#v, error = %v", fills, err)
	}
	if orders := sell.Orders(); len(orders) != 1 || orders[0].Status != domain.OrderStatusRejected {
		t.Fatalf("sell orders = %#v", orders)
	}
}

type onceProcessor struct {
	done bool
}

func (p *onceProcessor) Process(_ context.Context, event domain.MarketEvent) ([]domain.Signal, error) {
	if p.done {
		return nil, nil
	}
	p.done = true
	return []domain.Signal{{
		ID: "signal", StrategyID: "strategy", ExchangeAccountID: "backtest", InstrumentID: "TEST",
		Action: domain.SignalBuy, OrderType: domain.OrderTypeMarket, Quantity: quantity("1"),
		CreatedAt: event.ExchangeTime, CausativeCursor: domain.EventCursor{Timestamp: event.ExchangeTime, Priority: 50, Sequence: event.Sequence},
	}}, nil
}

type roundTripProcessor struct {
	event uint64
}

func (p *roundTripProcessor) Process(_ context.Context, event domain.MarketEvent) ([]domain.Signal, error) {
	p.event++
	var action domain.SignalAction
	switch p.event {
	case 1:
		action = domain.SignalBuy
	case 2:
		action = domain.SignalSell
	default:
		return nil, nil
	}
	return []domain.Signal{{
		ID:         domain.SignalID(fmt.Sprintf("round-trip-%d", p.event)),
		StrategyID: "strategy", ExchangeAccountID: "backtest", InstrumentID: "TEST",
		Action: action, OrderType: domain.OrderTypeMarket, Quantity: quantity("1"),
		CreatedAt: event.ExchangeTime,
		CausativeCursor: domain.EventCursor{
			Timestamp: event.ExchangeTime, Priority: 50, Sequence: event.Sequence,
		},
	}}, nil
}

func TestRunnerReproducibility(t *testing.T) {
	run := func() []byte {
		iterator, err := NewCSVIterator(fixture(t, "market-next-open.csv"), metadata(GapFail))
		if err != nil {
			t.Fatal(err)
		}
		broker := newBroker(t, "0.03", "5")
		report, err := (Runner{Iterator: iterator, Clock: clock.NewVirtual(time.Time{}),
			Strategy: &onceProcessor{}, Risk: AllowAllRisk{}, Broker: broker}).Run(context.Background())
		if err != nil {
			t.Fatal(err)
		}
		data, err := json.Marshal(report)
		if err != nil {
			t.Fatal(err)
		}
		return data
	}
	if left, right := run(), run(); !bytes.Equal(left, right) {
		t.Fatalf("reports differ:\n%s\n%s", left, right)
	}
}

func TestGoldenRunnerMetrics(t *testing.T) {
	iterator, err := NewCSVIterator(fixture(t, "market-next-open.csv"), metadata(GapFail))
	if err != nil {
		t.Fatal(err)
	}
	broker := newBroker(t, "0.03", "5")
	report, err := (Runner{Iterator: iterator, Clock: clock.NewVirtual(time.Time{}),
		Strategy: &onceProcessor{}, Risk: AllowAllRisk{}, Broker: broker}).Run(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	expected := Metrics{
		InitialEquity:  DecimalMoney{Amount: "1000", Asset: "USD"},
		FinalEquity:    DecimalMoney{Amount: "1001.906982", Asset: "USD"},
		TotalPnL:       DecimalMoney{Amount: "1.906982", Asset: "USD"},
		RealizedPnL:    DecimalMoney{Amount: "0", Asset: "USD"},
		UnrealizedPnL:  DecimalMoney{Amount: "1.94", Asset: "USD"},
		ReturnPercent:  "0.1906982",
		Commissions:    DecimalMoney{Amount: "0.033018", Asset: "USD"},
		SlippageCost:   DecimalMoney{Amount: "0.06", Asset: "USD"},
		MaxDrawdown:    DecimalMoney{Amount: "0", Asset: "USD"},
		MaxDrawdownPct: "0", Orders: 1, Fills: 1,
		WinRate: "0", GrossProfit: DecimalMoney{Amount: "0", Asset: "USD"},
		GrossLoss: DecimalMoney{Amount: "0", Asset: "USD"}, ProfitFactor: "0",
		ExposureTime: "1m0s", ExposurePct: "50",
	}
	left, _ := json.Marshal(report.Metrics)
	right, _ := json.Marshal(expected)
	if !bytes.Equal(left, right) {
		t.Fatalf("metrics mismatch:\n got %s\nwant %s", left, right)
	}
	if report.Orders[0].Status != domain.OrderStatusFilled {
		t.Fatalf("final order status %v", report.Orders[0].Status)
	}
}

func TestGoldenRoundTripMetrics(t *testing.T) {
	iterator, err := NewCSVIterator(fixture(t, "market-next-open.csv"), metadata(GapFail))
	if err != nil {
		t.Fatal(err)
	}
	report, err := (Runner{
		Iterator: iterator, Clock: clock.NewVirtual(time.Time{}),
		Strategy: &roundTripProcessor{}, Risk: AllowAllRisk{}, Broker: newBroker(t, "0", "0"),
	}).Run(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if report.Metrics.TotalPnL.Amount != "1" || report.Metrics.RealizedPnL.Amount != "1" ||
		report.Metrics.UnrealizedPnL.Amount != "0" || report.Metrics.ClosedTrades != 1 ||
		report.Metrics.WinningTrades != 1 || report.Metrics.LosingTrades != 0 ||
		report.Metrics.WinRate != "100" || report.Metrics.GrossProfit.Amount != "1" ||
		report.Metrics.GrossLoss.Amount != "0" || report.Metrics.ProfitFactor != "unbounded" {
		data, _ := json.Marshal(report.Metrics)
		t.Fatalf("round-trip metrics = %s", data)
	}
	if len(report.Executions) != 2 ||
		report.Executions[0].Price.Value.String() != "110" ||
		report.Executions[1].Price.Value.String() != "111" {
		t.Fatalf("executions = %#v", report.Executions)
	}
}

func newBroker(t *testing.T, commission, slippage string) *SimulatedBroker {
	t.Helper()
	broker, err := NewSimulatedBroker(BrokerConfig{
		InitialCash:  domain.Money{Amount: decimal.NewFromInt(1000), Asset: "USD"},
		InstrumentID: "TEST", TickSize: *price(t, "0.01"), LotSize: quantity("1"),
		CommissionPercent: decimal.RequireFromString(commission),
		SlippageBPS:       decimal.RequireFromString(slippage),
	})
	if err != nil {
		t.Fatal(err)
	}
	return broker
}

func marketRequest(causative time.Time) SubmitRequest {
	return SubmitRequest{
		OrderID: "order", ClientOrderID: "client", StrategyID: "strategy", InstrumentID: "TEST",
		Side: domain.OrderSideBuy, Type: domain.OrderTypeMarket, Quantity: quantity("1"),
		SubmittedAt: causative.UTC(), CausativeCursor: domain.EventCursor{Timestamp: causative.UTC(), Priority: 50, Sequence: 1},
	}
}

func candleEvent(t *testing.T, start, open, high, low, close string) domain.MarketEvent {
	t.Helper()
	at, err := time.Parse(time.RFC3339, start)
	if err != nil {
		t.Fatal(err)
	}
	candle := domain.Candle{Start: at, End: at.Add(time.Minute), Interval: time.Minute,
		Open: *price(t, open), High: *price(t, high), Low: *price(t, low), Close: *price(t, close),
		Volume: quantity("1"), Complete: true}
	return domain.MarketEvent{ExchangeAccountID: "backtest", InstrumentID: "TEST",
		Kind: domain.MarketEventCandleClose, ExchangeTime: candle.End, ReceivedTime: candle.End, Sequence: 1, Candle: &candle}
}

func price(t *testing.T, value string) *domain.Price {
	t.Helper()
	result, err := domain.NewPrice(value, "USD")
	if err != nil {
		t.Fatal(err)
	}
	return &result
}

func quantity(value string) domain.Quantity {
	result, _ := domain.NewQuantity(value)
	return result
}
