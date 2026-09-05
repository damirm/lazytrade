package tinvest

import (
	"errors"
	"testing"

	"github.com/damirm/lazytrade/internal/exchange"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

func TestMapMutationErrorClassifiesAmbiguousGRPCCodesAsUnknown(t *testing.T) {
	t.Parallel()
	for _, code := range []codes.Code{
		codes.Unknown, codes.Aborted, codes.DataLoss, codes.AlreadyExists,
		codes.Canceled, codes.Internal, codes.Unavailable, codes.DeadlineExceeded,
	} {
		code := code
		t.Run(code.String(), func(t *testing.T) {
			t.Parallel()
			cause := status.Error(code, "ambiguous")
			err := mapMutationError("mutate", cause)
			var exchangeErr *exchange.Error
			if !errors.As(err, &exchangeErr) || exchangeErr.Category != exchange.ErrorUnknownOutcome ||
				exchangeErr.Outcome != exchange.OutcomeUnknown || exchangeErr.Retryable {
				t.Fatalf("mapped error = %T %#v", err, err)
			}
			if !errors.Is(err, cause) {
				t.Fatal("mapped error does not preserve cause")
			}
		})
	}
}

func TestMapMutationErrorPreservesProvenRejections(t *testing.T) {
	t.Parallel()
	for _, code := range []codes.Code{
		codes.InvalidArgument, codes.Unauthenticated, codes.PermissionDenied,
		codes.NotFound, codes.ResourceExhausted, codes.FailedPrecondition,
	} {
		code := code
		t.Run(code.String(), func(t *testing.T) {
			t.Parallel()
			err := mapMutationError("mutate", status.Error(code, "rejected"))
			var exchangeErr *exchange.Error
			if !errors.As(err, &exchangeErr) || exchangeErr.Outcome != exchange.OutcomeKnownNotApplied {
				t.Fatalf("mapped error = %T %#v", err, err)
			}
		})
	}
}
