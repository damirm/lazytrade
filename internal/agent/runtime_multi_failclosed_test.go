package agent

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/damirm/lazytrade/internal/domain"
	"github.com/damirm/lazytrade/internal/exchange"
	"github.com/damirm/lazytrade/internal/exchange/fake"
)

type controlledExecutionExchange struct {
	exchange.Exchange
	executions chan domain.Execution
	errors     chan error
}

func (e *controlledExecutionExchange) SubscribeExecutions(
	context.Context,
	domain.ExchangeAccountID,
) (exchange.ExecutionStream, error) {
	return exchange.ExecutionStream{Executions: e.executions, Errors: e.errors}, nil
}

func TestMultiStrategyRuntimeFailsClosedWhenExecutionStreamBreaks(t *testing.T) {
	for _, test := range []struct {
		name    string
		breakIt func(chan domain.Execution, chan error)
		want    string
	}{
		{
			name: "reported error",
			breakIt: func(_ chan domain.Execution, streamErrors chan error) {
				streamErrors <- errors.New("execution transport failed")
			},
			want: "execution stream: execution transport failed",
		},
		{
			name: "execution channel closed",
			breakIt: func(executions chan domain.Execution, _ chan error) {
				close(executions)
			},
			want: "execution stream closed",
		},
		{
			name: "error channel closed",
			breakIt: func(_ chan domain.Execution, streamErrors chan error) {
				close(streamErrors)
			},
			want: "execution error stream closed",
		},
	} {
		t.Run(test.name, func(t *testing.T) {
			store, workers, strategyIDs, subscriptions, _ := seedTwoPendingSignals(t)
			base := fake.New("fake", exchange.Capabilities{StreamingCandles: true, Sandbox: true})
			executions := make(chan domain.Execution)
			streamErrors := make(chan error, 1)
			adapter := &controlledExecutionExchange{
				Exchange: base, executions: executions, errors: streamErrors,
			}
			ready := make(chan struct{}, 1)
			runCtx, cancel := context.WithCancel(context.Background())
			defer cancel()
			done := make(chan error, 1)
			risks := map[domain.StrategyID]SignalRisk{
				"ma-a": &multiRecoveryRisk{},
				"ma-b": &multiRecoveryRisk{},
			}
			go func() {
				done <- (Runtime{
					Exchange: adapter, Strategies: testStrategyBindings(t, workers, strategyIDs, risks, subscriptions),
					Intents: store, Ready: ready,
				}).Run(runCtx)
			}()
			select {
			case <-ready:
			case err := <-done:
				t.Fatalf("runtime stopped before ready: %v", err)
			case <-time.After(2 * time.Second):
				t.Fatal("runtime did not become ready")
			}

			test.breakIt(executions, streamErrors)
			select {
			case err := <-done:
				if err == nil || !strings.Contains(err.Error(), test.want) {
					t.Fatalf("Run() error = %v, want %q", err, test.want)
				}
			case <-time.After(2 * time.Second):
				t.Fatal("runtime did not fail closed")
			}
		})
	}
}
