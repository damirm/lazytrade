package cli

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/damirm/lazytrade/internal/domain"
	"github.com/damirm/lazytrade/internal/exchange"
	"github.com/shopspring/decimal"
)

type historyProbeProviderStub struct {
	calls    int
	requests []exchange.ExecutionHistoryRequest
	history  exchange.ExecutionHistory
	err      error
}

func TestAgentCommandExposesHistoryProbe(t *testing.T) {
	t.Parallel()
	for _, child := range newAgentCommand().Commands() {
		if child.Name() == "history-probe" {
			return
		}
	}
	t.Fatal("agent history-probe subcommand is missing")
}

func (s *historyProbeProviderStub) ExecutionHistory(
	_ context.Context,
	request exchange.ExecutionHistoryRequest,
) (exchange.ExecutionHistory, error) {
	s.calls++
	s.requests = append(s.requests, request)
	return s.history, s.err
}

func TestRunHistoryProbeValidatesBeforeExternalAccess(t *testing.T) {
	t.Parallel()
	validFrom := time.Date(2026, 8, 1, 0, 0, 0, 0, time.UTC)
	validTo := validFrom.Add(time.Hour)
	for _, test := range []struct {
		name      string
		accountID domain.ExchangeAccountID
		from      time.Time
		to        time.Time
	}{
		{name: "empty account", from: validFrom, to: validTo},
		{name: "zero from", accountID: "sandbox-account", to: validTo},
		{name: "zero to", accountID: "sandbox-account", from: validFrom},
		{name: "empty window", accountID: "sandbox-account", from: validFrom, to: validFrom},
		{name: "reversed window", accountID: "sandbox-account", from: validTo, to: validFrom},
		{name: "non UTC from", accountID: "sandbox-account", from: validFrom.In(time.FixedZone("test", 3*60*60)), to: validTo},
		{name: "non UTC to", accountID: "sandbox-account", from: validFrom, to: validTo.In(time.FixedZone("test", 3*60*60))},
	} {
		test := test
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()
			provider := &historyProbeProviderStub{}
			if _, err := runHistoryProbe(context.Background(), provider, test.accountID, test.from, test.to); err == nil {
				t.Fatal("runHistoryProbe error = nil")
			}
			if provider.calls != 0 {
				t.Fatalf("external history calls = %d, want 0", provider.calls)
			}
		})
	}
}

func TestRunHistoryProbeUsesExactBoundedWindow(t *testing.T) {
	t.Parallel()
	from := time.Date(2026, 8, 1, 6, 0, 0, 0, time.UTC)
	to := from.Add(72 * time.Hour)
	provider := &historyProbeProviderStub{history: exchange.ExecutionHistory{
		From: from, To: to, Complete: true,
	}}

	report, err := runHistoryProbe(context.Background(), provider, "sandbox-account", from, to)
	if err != nil {
		t.Fatal(err)
	}
	if provider.calls != 1 || len(provider.requests) != 1 {
		t.Fatalf("history calls = %d, requests = %#v", provider.calls, provider.requests)
	}
	request := provider.requests[0]
	if request.AccountID != "sandbox-account" || !request.From.Equal(from) || !request.To.Equal(to) {
		t.Fatalf("history request = %#v", request)
	}
	if report.AccountID != "sandbox-account" || !report.From.Equal(from) || !report.To.Equal(to) || !report.Complete {
		t.Fatalf("history report = %#v", report)
	}
}

func TestRunHistoryProbeRejectsNonEchoedOrIncompleteHistory(t *testing.T) {
	t.Parallel()
	from := time.Date(2026, 8, 1, 6, 0, 0, 0, time.UTC)
	to := from.Add(time.Hour)
	for _, test := range []struct {
		name    string
		history exchange.ExecutionHistory
	}{
		{name: "incomplete", history: exchange.ExecutionHistory{From: from, To: to, Complete: false}},
		{name: "from changed", history: exchange.ExecutionHistory{From: from.Add(time.Second), To: to, Complete: true}},
		{name: "to changed", history: exchange.ExecutionHistory{From: from, To: to.Add(-time.Second), Complete: true}},
	} {
		test := test
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()
			provider := &historyProbeProviderStub{history: test.history}
			if _, err := runHistoryProbe(context.Background(), provider, "sandbox-account", from, to); err == nil {
				t.Fatal("runHistoryProbe error = nil, want nonzero command outcome")
			}
			if provider.calls != 1 {
				t.Fatalf("history calls = %d, want 1", provider.calls)
			}
		})
	}
}

func TestRunHistoryProbePropagatesBridgeFailure(t *testing.T) {
	t.Parallel()
	from := time.Date(2026, 8, 1, 6, 0, 0, 0, time.UTC)
	bridgeErr := errors.New("history order cannot be bridged to order state")
	provider := &historyProbeProviderStub{err: bridgeErr}

	if _, err := runHistoryProbe(context.Background(), provider, "sandbox-account", from, from.Add(time.Hour)); !errors.Is(err, bridgeErr) {
		t.Fatalf("runHistoryProbe error = %v, want bridge error", err)
	}
	if provider.calls != 1 {
		t.Fatalf("history calls = %d, want 1", provider.calls)
	}
}

func TestHistoryProbeReportContainsSummaryWithoutSecretFields(t *testing.T) {
	t.Setenv("TINVEST_SANDBOX_TOKEN", "probe-super-secret-token")
	t.Setenv("LAZYTRADE_TEST_DSN", "postgres://secret-user:secret-password@host/database")
	from := time.Date(2026, 8, 1, 6, 0, 0, 0, time.UTC)
	to := from.Add(time.Hour)
	provider := &historyProbeProviderStub{history: exchange.ExecutionHistory{
		From: from, To: to, Complete: true,
		Orders: []exchange.RecoveredOrderSnapshot{{
			ExchangeOrderID: "exchange-order", ClientOrderID: "client-order",
			InstrumentID: "instrument", Side: domain.OrderSideBuy,
			OrderType: domain.OrderTypeMarket, RequestedQuantity: domain.Quantity{Value: decimal.NewFromInt(2)},
			Status: domain.OrderStatusFilled, SubmittedAt: from, Complete: true,
			CumulativeCommission: domain.Money{Amount: decimal.RequireFromString("1.5"), Asset: "RUB"},
			Fills: []exchange.RecoveredExecutionFill{{
				TradeID: "trade-1", Quantity: domain.Quantity{Value: decimal.NewFromInt(2)},
				Price: domain.Price{Value: decimal.NewFromInt(100), Asset: "RUB"}, ExecutedAt: from.Add(time.Minute),
			}},
		}},
	}}

	report, err := runHistoryProbe(context.Background(), provider, "sandbox-account", from, to)
	if err != nil {
		t.Fatal(err)
	}
	payload, err := json.Marshal(report)
	if err != nil {
		t.Fatal(err)
	}
	output := string(payload)
	for _, forbidden := range []string{
		"probe-super-secret-token", "secret-password", `"token"`, `"dsn"`, "authorization",
	} {
		if strings.Contains(strings.ToLower(output), strings.ToLower(forbidden)) {
			t.Fatalf("history probe output contains forbidden value %q: %s", forbidden, output)
		}
	}
	for _, required := range []string{
		`"account_id"`, `"from"`, `"to"`, `"complete"`, `"orders"`, `"fills"`, `"cumulative_commission"`,
	} {
		if !strings.Contains(output, required) {
			t.Fatalf("history probe output misses %s: %s", required, output)
		}
	}
}
