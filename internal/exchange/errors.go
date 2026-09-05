package exchange

import (
	"errors"
	"fmt"
)

type ErrorCategory uint8

const (
	ErrorInvalidRequest ErrorCategory = iota + 1
	ErrorAuthentication
	ErrorPermission
	ErrorNotFound
	ErrorInsufficientFunds
	ErrorRateLimited
	ErrorTransient
	ErrorUnknownOutcome
	ErrorRejected
	ErrorPermanent
	ErrorCanceled
)

type Outcome uint8

const (
	OutcomeKnownNotApplied Outcome = iota + 1
	OutcomeUnknown
)

// Error is the transport-independent error returned by exchange adapters.
// Message must never contain credentials or raw request metadata.
type Error struct {
	Operation string
	Category  ErrorCategory
	Outcome   Outcome
	Retryable bool
	Code      string
	Message   string
	Cause     error
}

func (e *Error) Error() string {
	if e == nil {
		return "<nil>"
	}
	message := e.Message
	if message == "" {
		message = "exchange operation failed"
	}
	if e.Code != "" {
		return fmt.Sprintf("%s: %s (%s)", e.Operation, message, e.Code)
	}
	return fmt.Sprintf("%s: %s", e.Operation, message)
}

func (e *Error) Unwrap() error { return e.Cause }

func IsCategory(err error, category ErrorCategory) bool {
	var exchangeErr *Error
	return errors.As(err, &exchangeErr) && exchangeErr.Category == category
}
