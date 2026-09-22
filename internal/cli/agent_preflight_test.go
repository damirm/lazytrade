package cli

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/damirm/lazytrade/internal/domain"
	"github.com/damirm/lazytrade/internal/exchange"
	"github.com/shopspring/decimal"
)

func TestCheckOpenedStreamsDetectsAvailableFailures(t *testing.T) {
	for _, test := range []struct {
		name    string
		breakIt func(chan error, chan error)
		want    string
	}{
		{"open without market activity", func(chan error, chan error) {}, ""},
		{"market error", func(market chan error, _ chan error) { market <- errors.New("failed") }, "preflight market stream: failed"},
		{"execution error", func(_ chan error, executions chan error) { executions <- errors.New("failed") }, "preflight execution stream: failed"},
		{"market closed", func(market chan error, _ chan error) { close(market) }, "preflight market stream closed"},
		{"execution closed", func(_ chan error, executions chan error) { close(executions) }, "preflight execution stream closed"},
	} {
		t.Run(test.name, func(t *testing.T) {
			market, executions := make(chan error, 1), make(chan error, 1)
			test.breakIt(market, executions)
			err := checkOpenedStreams(context.Background(), exchange.MarketStream{Errors: market}, exchange.ExecutionStream{Errors: executions})
			if test.want == "" {
				if err != nil {
					t.Fatal(err)
				}
			} else if err == nil || !strings.Contains(err.Error(), test.want) {
				t.Fatalf("error = %v, want %q", err, test.want)
			}
		})
	}
}

func TestCheckOpenedStreamsHonorsCancellation(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if err := checkOpenedStreams(ctx, exchange.MarketStream{}, exchange.ExecutionStream{}); !errors.Is(err, context.Canceled) {
		t.Fatalf("error = %v, want canceled", err)
	}
}

func TestValidateWholeLots(t *testing.T) {
	lot := domain.Quantity{Value: decimal.NewFromInt(10)}
	if err := validateWholeLots(domain.Quantity{Value: decimal.NewFromInt(20)}, lot); err != nil {
		t.Fatal(err)
	}
	err := validateWholeLots(domain.Quantity{Value: decimal.NewFromInt(1)}, lot)
	if err == nil || !strings.Contains(err.Error(), "lot size is 10") {
		t.Fatalf("error = %v", err)
	}
}

func TestAgentCommandExposesPreflight(t *testing.T) {
	command := newAgentCommand()
	for _, child := range command.Commands() {
		if child.Name() == "preflight" {
			return
		}
	}
	t.Fatal("agent preflight subcommand is missing")
}
