package agent

import (
	"context"
	"errors"
	"fmt"
	"sort"
	"time"

	"github.com/damirm/lazytrade/internal/domain"
	"github.com/damirm/lazytrade/internal/exchange"
	"github.com/damirm/lazytrade/internal/storage"
	"github.com/damirm/lazytrade/internal/strategy"
)

const (
	RuntimeStatusReconciling = "reconciling"
	RuntimeStatusRunning     = "running"
	RuntimeStatusStopped     = "stopped"
	RuntimeStatusFailed      = "failed"
	RuntimeStatusBlocked     = "blocked"
)

type LifecycleStore interface {
	SetStrategyStatus(context.Context, domain.StrategyID, string, string, time.Time) error
}

type lifecycleRecoveryStore interface {
	LoadStrategyLifecycle(context.Context, domain.StrategyID) (storage.StrategyLifecycle, error)
}

type blockedRuntimeError struct{ err error }

func (e blockedRuntimeError) Error() string { return e.err.Error() }
func (e blockedRuntimeError) Unwrap() error { return e.err }

func blockRuntime(err error) error {
	if err == nil {
		return nil
	}
	return blockedRuntimeError{err: err}
}

type StrategyRuntimeError struct {
	StrategyID domain.StrategyID
	Operation  string
	Err        error
}

func (e *StrategyRuntimeError) Error() string {
	return fmt.Sprintf("strategy %s %s: %v", e.StrategyID, e.Operation, e.Err)
}

func (e *StrategyRuntimeError) Unwrap() error { return e.Err }

func strategyRuntimeError(strategyID domain.StrategyID, operation string, err error) error {
	if err == nil || strategyID.Validate() != nil {
		return err
	}
	return &StrategyRuntimeError{StrategyID: strategyID, Operation: operation, Err: err}
}

func runtimeTerminalStatus(ctx context.Context, runErr error) (string, string) {
	if errors.Is(runErr, context.Canceled) || ctx.Err() != nil {
		return RuntimeStatusStopped, ""
	}
	var blocked blockedRuntimeError
	if errors.As(runErr, &blocked) {
		return RuntimeStatusBlocked, runErr.Error()
	}
	return RuntimeStatusFailed, runErr.Error()
}

func (r Runtime) lifecycleStrategyIDs() []domain.StrategyID {
	unique := make(map[domain.StrategyID]struct{}, len(r.Strategies))
	for _, binding := range r.Strategies {
		if binding.ID.Validate() == nil {
			unique[binding.ID] = struct{}{}
		}
	}
	result := make([]domain.StrategyID, 0, len(unique))
	for strategyID := range unique {
		result = append(result, strategyID)
	}
	sort.Slice(result, func(i, j int) bool { return result[i] < result[j] })
	return result
}

func (r Runtime) setLifecycle(
	ctx context.Context,
	strategyIDs []domain.StrategyID,
	status string,
	reason string,
) error {
	if r.Lifecycle == nil {
		return nil
	}
	now := time.Now().UTC()
	if r.Now != nil {
		now = r.Now().UTC()
	}
	var result error
	for _, strategyID := range strategyIDs {
		err := r.Lifecycle.SetStrategyStatus(ctx, strategyID, status, reason, now)
		if err != nil {
			result = errors.Join(result, fmt.Errorf("strategy %s lifecycle %s: %w", strategyID, status, err))
			continue
		}
		r.logger().InfoContext(ctx, "strategy lifecycle changed", "event", "strategy_lifecycle_changed", "strategy_id", strategyID, "status", status)
	}
	return result
}

func (r Runtime) repairFailedStrategySignals(
	ctx context.Context,
	strategyIDs []domain.StrategyID,
) error {
	recovery, ok := r.Lifecycle.(lifecycleRecoveryStore)
	if !ok {
		return nil
	}
	for _, strategyID := range strategyIDs {
		state, err := recovery.LoadStrategyLifecycle(ctx, strategyID)
		if errors.Is(err, storage.ErrNotFound) {
			continue
		}
		if err != nil {
			return fmt.Errorf("load strategy %s lifecycle: %w", strategyID, err)
		}
		if state.Status == RuntimeStatusFailed {
			if err := r.terminalizeFailedStrategySignals(ctx, strategyID); err != nil {
				return fmt.Errorf("repair strategy %s pending signals: %w", strategyID, err)
			}
		}
	}
	return nil
}

func (r Runtime) setTerminalLifecycle(
	ctx context.Context,
	strategyIDs []domain.StrategyID,
	runErr error,
) error {
	var strategyFailure *StrategyRuntimeError
	if errors.As(runErr, &strategyFailure) {
		var result error
		for _, strategyID := range strategyIDs {
			status := RuntimeStatusBlocked
			reason := fmt.Sprintf(
				"account runtime stopped after strategy %s failed: %v",
				strategyFailure.StrategyID,
				strategyFailure.Err,
			)
			if strategyID == strategyFailure.StrategyID {
				status = RuntimeStatusFailed
				reason = strategyFailure.Error()
			}
			result = errors.Join(result, r.setLifecycle(ctx, []domain.StrategyID{strategyID}, status, reason))
		}
		return result
	}
	status, reason := runtimeTerminalStatus(ctx, runErr)
	return r.setLifecycle(ctx, strategyIDs, status, reason)
}

func (r Runtime) isolateStrategyFailure(
	ctx context.Context,
	runErr error,
	activeWorkers map[domain.InstrumentID]*strategy.Worker,
	failed map[domain.StrategyID]struct{},
) (bool, error) {
	var strategyFailure *StrategyRuntimeError
	if !errors.As(runErr, &strategyFailure) {
		return false, nil
	}
	if _, alreadyFailed := failed[strategyFailure.StrategyID]; alreadyFailed {
		return true, nil
	}
	for _, binding := range r.Strategies {
		if binding.ID == strategyFailure.StrategyID {
			delete(activeWorkers, binding.InstrumentID)
		}
	}
	if err := r.terminalizeFailedStrategySignals(ctx, strategyFailure.StrategyID); err != nil {
		return false, fmt.Errorf("terminalize failed strategy signals: %w", err)
	}
	if err := r.resolveFailedStrategyIntents(ctx, strategyFailure.StrategyID); err != nil {
		return false, err
	}
	if err := r.setLifecycle(
		ctx,
		[]domain.StrategyID{strategyFailure.StrategyID},
		RuntimeStatusFailed,
		strategyFailure.Error(),
	); err != nil {
		return false, fmt.Errorf("persist failed strategy lifecycle: %w", err)
	}
	failed[strategyFailure.StrategyID] = struct{}{}
	r.logger().ErrorContext(ctx, "strategy isolated after failure", "event", "strategy_failed", "strategy_id", strategyFailure.StrategyID, "error", strategyFailure)
	if r.OnStrategyError != nil {
		r.OnStrategyError(strategyFailure.StrategyID, strategyFailure)
	}
	return true, nil
}

func (r Runtime) resolveFailedStrategyIntents(
	ctx context.Context,
	strategyID domain.StrategyID,
) error {
	const limit uint32 = 1000
	intents, err := r.Intents.ListPendingOrderIntentsByStrategy(ctx, strategyID, limit)
	if err != nil {
		return blockRuntime(fmt.Errorf("list unresolved intents for strategy %s: %w", strategyID, err))
	}
	if len(intents) == int(limit) {
		return blockRuntime(fmt.Errorf("strategy %s has too many unresolved intents", strategyID))
	}
	for _, intent := range intents {
		if intent.Status == "ready" {
			now := time.Now().UTC()
			if r.Now != nil {
				now = r.Now().UTC()
			}
			if err := r.Intents.TransitionOrderIntent(ctx, storage.IntentTransition{
				IntentID: intent.ID, FromStatus: "ready", ToStatus: "not_submitted",
				Audit: intentTransitionAudit(
					intent, "ready", "not_submitted", "strategy_failed_before_submission",
					"strategy failed before the exchange API boundary", now,
				),
			}); err != nil {
				return blockRuntime(fmt.Errorf("terminalize ready intent %s: %w", intent.ID, err))
			}
			continue
		}
		order, lookupErr := r.Exchange.GetOrderByClientID(ctx, intent.ClientOrderID)
		if lookupErr == nil {
			if err := r.recordSubmitted(ctx, intent, order, "recovered_after_strategy_failure"); err != nil {
				return blockRuntime(err)
			}
			continue
		}
		if exchange.IsCategory(lookupErr, exchange.ErrorNotFound) {
			return blockRuntime(fmt.Errorf(
				"strategy %s intent %s has unresolved %s submission outcome: client order %s was not found",
				strategyID, intent.ID, intent.Status, intent.ClientOrderID,
			))
		}
		return blockRuntime(fmt.Errorf(
			"strategy %s intent %s lookup is inconclusive: %w",
			strategyID, intent.ID, lookupErr,
		))
	}
	return nil
}

func (r Runtime) terminalizeFailedStrategySignals(
	ctx context.Context,
	strategyID domain.StrategyID,
) error {
	const batchSize uint32 = 100
	for {
		signals, err := r.Intents.ListSignalsPendingRiskByStrategy(ctx, strategyID, batchSize)
		if err != nil {
			return err
		}
		for _, signal := range signals {
			decision, audit, err := buildRiskDecision(signal, RiskDecision{
				Allowed:    false,
				ReasonCode: "strategy_failed",
				Reason:     "strategy worker failed before risk processing completed",
			})
			if err != nil {
				return err
			}
			if err := r.Intents.RecordRiskDecision(ctx, decision, audit); err != nil {
				return err
			}
		}
		if len(signals) < int(batchSize) {
			return nil
		}
	}
}
