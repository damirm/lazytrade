package tinvest

import (
	"context"
	"errors"

	"github.com/damirm/lazytrade/internal/exchange"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

func mapError(operation string, err error) error {
	if err == nil {
		return nil
	}
	category, retry := exchange.ErrorPermanent, false
	switch {
	case errors.Is(err, context.Canceled):
		category = exchange.ErrorCanceled
	case status.Code(err) == codes.InvalidArgument:
		category = exchange.ErrorInvalidRequest
	case status.Code(err) == codes.Unauthenticated:
		category = exchange.ErrorAuthentication
	case status.Code(err) == codes.PermissionDenied:
		category = exchange.ErrorPermission
	case status.Code(err) == codes.NotFound:
		category = exchange.ErrorNotFound
	case status.Code(err) == codes.ResourceExhausted:
		category, retry = exchange.ErrorRateLimited, true
	case status.Code(err) == codes.Unavailable || status.Code(err) == codes.DeadlineExceeded || status.Code(err) == codes.Internal:
		category, retry = exchange.ErrorTransient, true
	case status.Code(err) == codes.FailedPrecondition:
		category = exchange.ErrorRejected
	}
	return &exchange.Error{
		Operation: operation, Category: category, Outcome: exchange.OutcomeKnownNotApplied,
		Retryable: retry, Code: status.Code(err).String(), Message: status.Convert(err).Message(), Cause: err,
	}
}

// mapMutationError treats transport failures after dispatch as an unknown
// outcome. The caller must resolve the idempotency key before any retry.
func mapMutationError(operation string, err error) error {
	mapped := mapError(operation, err)
	var exchangeErr *exchange.Error
	if errors.As(mapped, &exchangeErr) && !mutationKnownNotApplied(exchangeErr.Category) {
		exchangeErr.Category = exchange.ErrorUnknownOutcome
		exchangeErr.Outcome = exchange.OutcomeUnknown
		exchangeErr.Retryable = false
	}
	return mapped
}

func mutationKnownNotApplied(category exchange.ErrorCategory) bool {
	switch category {
	case exchange.ErrorInvalidRequest,
		exchange.ErrorAuthentication,
		exchange.ErrorPermission,
		exchange.ErrorNotFound,
		exchange.ErrorInsufficientFunds,
		exchange.ErrorRateLimited,
		exchange.ErrorRejected:
		return true
	default:
		return false
	}
}

// mutationResponseError represents a mutation accepted by the transport whose
// response cannot be safely interpreted. The remote side may already have
// applied it, so reconciliation is required before any retry.
func mutationResponseError(operation string, err error) error {
	if err == nil {
		return nil
	}
	return &exchange.Error{
		Operation: operation,
		Category:  exchange.ErrorUnknownOutcome,
		Outcome:   exchange.OutcomeUnknown,
		Retryable: false,
		Code:      "MALFORMED_RESPONSE",
		Message:   err.Error(),
		Cause:     err,
	}
}
