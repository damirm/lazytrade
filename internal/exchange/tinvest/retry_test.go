package tinvest

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/damirm/lazytrade/internal/domain"
	"github.com/damirm/lazytrade/internal/exchange"
	"github.com/shopspring/decimal"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
	pb "opensource.tbank.ru/invest/invest-go/proto"
)

func immediateReadRetryPolicy(attempts int) readRetryPolicy {
	return readRetryPolicy{MaxAttempts: attempts, Backoff: func(int) time.Duration { return 0 }}
}

func TestRetryReadRetriesTransientGRPCErrorsThenSucceeds(t *testing.T) {
	t.Parallel()
	for _, code := range []codes.Code{codes.Internal, codes.Unavailable, codes.DeadlineExceeded} {
		code := code
		t.Run(code.String(), func(t *testing.T) {
			t.Parallel()
			calls := 0
			got, err := retryRead(
				context.Background(), "test read", time.Second, immediateReadRetryPolicy(3),
				func(context.Context) (string, error) {
					calls++
					if calls < 3 {
						return "", status.Error(code, "temporary")
					}
					return "ok", nil
				},
			)
			if err != nil || got != "ok" {
				t.Fatalf("retryRead = %q, %v", got, err)
			}
			if calls != 3 {
				t.Fatalf("read calls = %d, want 3", calls)
			}
		})
	}
}

func TestRetryReadReturnsLastMappedErrorAfterExhaustion(t *testing.T) {
	t.Parallel()
	calls := 0
	_, err := retryRead(
		context.Background(), "exhausted read", time.Second, immediateReadRetryPolicy(3),
		func(context.Context) (struct{}, error) {
			calls++
			return struct{}{}, status.Error(codes.Unavailable, "still unavailable")
		},
	)
	if calls != 3 {
		t.Fatalf("read calls = %d, want 3", calls)
	}
	var exchangeErr *exchange.Error
	if !errors.As(err, &exchangeErr) || exchangeErr.Category != exchange.ErrorTransient || !exchangeErr.Retryable {
		t.Fatalf("exhausted error = %T %#v", err, err)
	}
	if exchangeErr.Operation != "exhausted read" || exchangeErr.Code != codes.Unavailable.String() {
		t.Fatalf("mapped exhausted error = %#v", exchangeErr)
	}
}

func TestRetryReadDoesNotRetryPermanentFailure(t *testing.T) {
	t.Parallel()
	calls := 0
	_, err := retryRead(
		context.Background(), "permanent read", time.Second, immediateReadRetryPolicy(3),
		func(context.Context) (struct{}, error) {
			calls++
			return struct{}{}, status.Error(codes.InvalidArgument, "invalid")
		},
	)
	if calls != 1 {
		t.Fatalf("read calls = %d, want 1", calls)
	}
	var exchangeErr *exchange.Error
	if !errors.As(err, &exchangeErr) || exchangeErr.Category != exchange.ErrorInvalidRequest || exchangeErr.Retryable {
		t.Fatalf("permanent error = %T %#v", err, err)
	}
}

func TestRetryReadContextCancellationInterruptsBackoff(t *testing.T) {
	t.Parallel()
	ctx, cancel := context.WithCancel(context.Background())
	firstAttempt := make(chan struct{})
	calls := 0
	done := make(chan error, 1)
	go func() {
		_, err := retryRead(
			ctx, "cancel read", time.Second,
			readRetryPolicy{MaxAttempts: 3, Backoff: func(int) time.Duration { return time.Hour }},
			func(context.Context) (struct{}, error) {
				calls++
				if calls == 1 {
					close(firstAttempt)
				}
				return struct{}{}, status.Error(codes.Unavailable, "temporary")
			},
		)
		done <- err
	}()
	<-firstAttempt
	cancel()
	select {
	case err := <-done:
		var exchangeErr *exchange.Error
		if !errors.As(err, &exchangeErr) || exchangeErr.Category != exchange.ErrorCanceled || exchangeErr.Retryable {
			t.Fatalf("canceled error = %T %#v", err, err)
		}
	case <-time.After(time.Second):
		t.Fatal("retry backoff ignored context cancellation")
	}
	if calls != 1 {
		t.Fatalf("read calls after cancellation = %d, want 1", calls)
	}
}

func TestExecutionHistoryRetriesReadOnlyPage(t *testing.T) {
	t.Parallel()
	from := time.Date(2026, 8, 1, 0, 0, 0, 0, time.UTC)
	stub := &sandboxStub{
		historyErrors:    []error{status.Error(codes.Unavailable, "temporary"), nil},
		historyResponses: []*pb.GetOperationsByCursorResponse{{}},
	}
	adapter := orderTestAdapter(stub)
	adapter.readRetry = immediateReadRetryPolicy(3)
	history, err := adapter.ExecutionHistory(context.Background(), exchange.ExecutionHistoryRequest{
		AccountID: "sandbox-account", From: from, To: from.Add(time.Hour),
	})
	if err != nil {
		t.Fatal(err)
	}
	if !history.Complete || len(stub.historyRequests) != 2 {
		t.Fatalf("history = %#v, requests = %d", history, len(stub.historyRequests))
	}
}

func TestPlaceOrderMutationTransportFailureIsAttemptedExactlyOnce(t *testing.T) {
	t.Parallel()
	for _, code := range []codes.Code{codes.Internal, codes.Unavailable, codes.DeadlineExceeded} {
		code := code
		t.Run(code.String(), func(t *testing.T) {
			t.Parallel()
			stub := &sandboxStub{postErr: status.Error(code, "ambiguous mutation")}
			adapter := orderTestAdapter(stub)
			adapter.readRetry = immediateReadRetryPolicy(3)
			_, err := adapter.PlaceOrder(context.Background(), exchange.NewOrder{
				ClientOrderID: "123e4567-e89b-52d3-a456-426614174000", StrategyID: "ma",
				ExchangeAccountID: "sandbox-account", InstrumentID: "instrument",
				Side: domain.OrderSideBuy, Type: domain.OrderTypeMarket,
				Quantity: domain.Quantity{Value: decimal.NewFromInt(10)},
			})
			if stub.postCalls != 1 {
				t.Fatalf("PostOrder calls = %d, want exactly 1", stub.postCalls)
			}
			var exchangeErr *exchange.Error
			if !errors.As(err, &exchangeErr) || exchangeErr.Outcome != exchange.OutcomeUnknown || exchangeErr.Retryable {
				t.Fatalf("mutation error = %T %#v", err, err)
			}
		})
	}
}

func TestOtherMutationsTransportFailureIsAttemptedExactlyOnce(t *testing.T) {
	t.Parallel()
	for _, code := range []codes.Code{codes.Internal, codes.Unavailable, codes.DeadlineExceeded} {
		code := code
		for _, mutation := range []struct {
			name  string
			call  func(*Adapter) error
			stub  func(*sandboxStub)
			calls func(*sandboxStub) int
		}{
			{
				name: "cancel order",
				call: func(adapter *Adapter) error {
					return adapter.CancelOrder(context.Background(), "exchange-order")
				},
				stub:  func(stub *sandboxStub) { stub.cancelErr = status.Error(code, "ambiguous cancel") },
				calls: func(stub *sandboxStub) int { return stub.cancelCalls },
			},
			{
				name: "open sandbox account",
				call: func(adapter *Adapter) error {
					_, err := adapter.CreateSandboxAccount(context.Background(), "retry-test")
					return err
				},
				stub:  func(stub *sandboxStub) { stub.openErr = status.Error(code, "ambiguous open") },
				calls: func(stub *sandboxStub) int { return stub.openCalls },
			},
			{
				name: "sandbox pay in",
				call: func(adapter *Adapter) error {
					_, err := adapter.PayInSandbox(context.Background(), "broker-account", domain.Money{
						Amount: decimal.NewFromInt(100), Asset: "RUB",
					})
					return err
				},
				stub:  func(stub *sandboxStub) { stub.payInErr = status.Error(code, "ambiguous pay in") },
				calls: func(stub *sandboxStub) int { return stub.payInCalls },
			},
		} {
			mutation := mutation
			t.Run(code.String()+"/"+mutation.name, func(t *testing.T) {
				t.Parallel()
				stub := &sandboxStub{}
				mutation.stub(stub)
				adapter := orderTestAdapter(stub)
				adapter.readRetry = immediateReadRetryPolicy(3)
				err := mutation.call(adapter)
				if mutation.calls(stub) != 1 {
					t.Fatalf("mutation calls = %d, want exactly 1", mutation.calls(stub))
				}
				var exchangeErr *exchange.Error
				if !errors.As(err, &exchangeErr) || exchangeErr.Outcome != exchange.OutcomeUnknown ||
					exchangeErr.Category != exchange.ErrorUnknownOutcome || exchangeErr.Retryable {
					t.Fatalf("mutation error = %T %#v", err, err)
				}
			})
		}
	}
}
