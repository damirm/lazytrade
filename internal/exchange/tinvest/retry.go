package tinvest

import (
	"context"
	"errors"
	"math/rand/v2"
	"time"

	"github.com/damirm/lazytrade/internal/exchange"
)

type readRetryPolicy struct {
	MaxAttempts int
	Backoff     func(retry int) time.Duration
}

var defaultReadRetryPolicy = readRetryPolicy{
	MaxAttempts: 3,
	Backoff: func(retry int) time.Duration {
		delay := 100 * time.Millisecond
		for i := 1; i < retry && delay < time.Second; i++ {
			delay *= 2
		}
		if delay > time.Second {
			delay = time.Second
		}
		// Full bounded jitter in [80%, 120%].
		return delay*8/10 + time.Duration(rand.Int64N(int64(delay*4/10)+1))
	},
}

func retryRead[T any](
	ctx context.Context,
	operation string,
	perAttemptTimeout time.Duration,
	policy readRetryPolicy,
	call func(context.Context) (T, error),
) (T, error) {
	var zero T
	if policy.MaxAttempts <= 0 {
		policy.MaxAttempts = 1
	}
	if perAttemptTimeout <= 0 {
		perAttemptTimeout = 10 * time.Second
	}
	for attempt := 1; attempt <= policy.MaxAttempts; attempt++ {
		attemptCtx, cancel := context.WithTimeout(ctx, perAttemptTimeout)
		value, err := call(attemptCtx)
		cancel()
		if err == nil {
			return value, nil
		}
		mapped := mapError(operation, err)
		var exchangeErr *exchange.Error
		if attempt == policy.MaxAttempts || !errors.As(mapped, &exchangeErr) ||
			!exchangeErr.Retryable ||
			(exchangeErr.Category != exchange.ErrorTransient && exchangeErr.Category != exchange.ErrorRateLimited) {
			return zero, mapped
		}
		delay := time.Duration(0)
		if policy.Backoff != nil {
			delay = policy.Backoff(attempt)
		}
		if delay <= 0 {
			continue
		}
		timer := time.NewTimer(delay)
		select {
		case <-ctx.Done():
			if !timer.Stop() {
				<-timer.C
			}
			return zero, mapError(operation, ctx.Err())
		case <-timer.C:
		}
	}
	return zero, mapError(operation, context.Canceled)
}
