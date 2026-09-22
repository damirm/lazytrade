package agent

import (
	"context"
	"errors"
	"fmt"
	"sync"
	"time"

	"github.com/damirm/lazytrade/internal/domain"
	"github.com/damirm/lazytrade/internal/exchange"
	"github.com/damirm/lazytrade/internal/storage"
	"github.com/damirm/lazytrade/internal/strategy"
)

type startupResult struct {
	accountID              domain.ExchangeAccountID
	activeWorkers          map[domain.InstrumentID]*strategy.Worker
	risks                  map[domain.StrategyID]SignalRisk
	marketStream           exchange.MarketStream
	executionNotifications <-chan struct{}
	executionPumpErrors    <-chan error
	executionStoreMu       *sync.Mutex
	pendingObservers       map[domain.StrategyID]struct{}
	signalsRecovered       bool
}

func (r Runtime) startup(
	ctx context.Context,
	workers map[domain.InstrumentID]*strategy.Worker,
	risks map[domain.StrategyID]SignalRisk,
	subscriptions []exchange.Subscription,
	lifecycleIDs []domain.StrategyID,
) (startupResult, error) {
	var zero startupResult
	logger := r.logger()
	activeWorkers := make(map[domain.InstrumentID]*strategy.Worker, len(workers))
	for instrumentID, worker := range workers {
		activeWorkers[instrumentID] = worker
	}
	accountID := domain.ExchangeAccountID(r.Exchange.Name())
	if err := accountID.Validate(); err != nil {
		return zero, fmt.Errorf("agent exchange account: %w", err)
	}
	missingIntents, err := r.resolvePendingIntents(ctx)
	if err != nil {
		return zero, fmt.Errorf("resolve pending intents: %w", err)
	}
	logger.InfoContext(ctx, "pending intents resolved", "event", "intent_recovery_completed", "ready", len(missingIntents))
	executionStream, err := r.Exchange.SubscribeExecutions(ctx, accountID)
	if err != nil {
		return zero, blockRuntime(fmt.Errorf("subscribe executions: %w", err))
	}
	logger.InfoContext(ctx, "execution stream subscribed", "event", "execution_stream_subscribed")
	executionStoreMu := &sync.Mutex{}
	executionNotifications, executionPumpErrors := r.startExecutionPump(ctx, accountID, executionStream, executionStoreMu)
	recoveryTo := time.Now().UTC()
	if r.Now != nil {
		recoveryTo = r.Now().UTC()
	}
	visibilityDelay := r.HistoryVisibilityDelay
	if visibilityDelay < 0 {
		return zero, errors.New("history visibility delay must not be negative")
	}
	recoveryTo = recoveryTo.Add(-visibilityDelay)
	checkpoint, recoveredHistory, err := r.recoverExecutionHistory(ctx, accountID, recoveryTo, executionStoreMu)
	if err != nil {
		return zero, blockRuntime(fmt.Errorf("recover execution history: %w", err))
	}
	logger.InfoContext(ctx, "execution history recovered", "event", "execution_history_recovered", "available", recoveredHistory)
	if err := r.drainPendingExecutionsSynchronized(ctx, accountID, executionStoreMu); err != nil {
		return zero, blockRuntime(fmt.Errorf("drain pending executions: %w", err))
	}
	if r.Reconciler != nil {
		if _, err := r.Reconciler.Reconcile(ctx, accountID); err != nil {
			return zero, blockRuntime(fmt.Errorf("startup reconciliation: %w", err))
		}
	}
	if recoveredHistory {
		if err := r.Intents.AdvanceExecutionHistoryCheckpoint(ctx, checkpoint); err != nil {
			return zero, blockRuntime(fmt.Errorf("advance execution history checkpoint: %w", err))
		}
	}
	// Only ready intents, durably proven not to have crossed the API boundary,
	// may be submitted, and only after the execution stream has been opened.
	for _, intent := range missingIntents {
		if err := r.submitIntent(ctx, intent, requestForIntent(intent)); err != nil {
			return zero, fmt.Errorf("submit pending intent %s: %w", intent.ID, err)
		}
	}
	// Close the snapshot/subscription gap before signals may produce orders.
	if r.Reconciler != nil {
		if _, err := r.Reconciler.Reconcile(ctx, accountID); err != nil {
			return zero, blockRuntime(fmt.Errorf("post-subscription reconciliation: %w", err))
		}
	}
	pendingObservers := make(map[domain.StrategyID]struct{})
	for strategyID, riskGate := range risks {
		if _, observesMarket := riskGate.(MarketRiskObserver); observesMarket {
			pendingObservers[strategyID] = struct{}{}
		}
	}
	signalsRecovered := false
	if len(pendingObservers) == 0 {
		if err := r.recoverSignalsWith(ctx, risks); err != nil {
			return zero, fmt.Errorf("recover pending signals: %w", err)
		}
		signalsRecovered = true
	}
	stream, err := r.Exchange.SubscribeMarketData(ctx, subscriptions)
	if err != nil {
		return zero, blockRuntime(fmt.Errorf("subscribe market data: %w", err))
	}
	logger.InfoContext(ctx, "market data subscribed", "event", "market_data_subscribed", "subscriptions", len(subscriptions))
	if err := r.setLifecycle(ctx, lifecycleIDs, RuntimeStatusRunning, ""); err != nil {
		return zero, fmt.Errorf("persist running lifecycle: %w", err)
	}
	logger.InfoContext(ctx, "agent running", "event", "agent_running", "strategies", len(workers))
	if r.Ready != nil {
		select {
		case r.Ready <- struct{}{}:
		case <-ctx.Done():
			return zero, ctx.Err()
		}
	}
	return startupResult{
		accountID:              accountID,
		activeWorkers:          activeWorkers,
		risks:                  risks,
		marketStream:           stream,
		executionNotifications: executionNotifications,
		executionPumpErrors:    executionPumpErrors,
		executionStoreMu:       executionStoreMu,
		pendingObservers:       pendingObservers,
		signalsRecovered:       signalsRecovered,
	}, nil
}

func (r Runtime) resolvePendingIntents(ctx context.Context) ([]storage.OrderIntent, error) {
	const limit = 1_000
	intents, err := r.Intents.ListPendingOrderIntents(ctx, limit)
	if err != nil {
		return nil, err
	}
	if len(intents) == limit {
		return nil, errors.New("too many pending order intents to recover safely")
	}
	ready := make([]storage.OrderIntent, 0, len(intents))
	for _, intent := range intents {
		if intent.Status == "ready" {
			ready = append(ready, intent)
			continue
		}
		order, lookupErr := r.Exchange.GetOrderByClientID(ctx, intent.ClientOrderID)
		switch {
		case lookupErr == nil:
			if err := r.recordSubmitted(ctx, intent, order, "recovered"); err != nil {
				return nil, blockRuntime(err)
			}
		case exchange.IsCategory(lookupErr, exchange.ErrorNotFound):
			return nil, blockRuntime(fmt.Errorf(
				"persisted intent %s has unresolved %s submission outcome: client order %s was not found",
				intent.ID, intent.Status, intent.ClientOrderID,
			))
		default:
			return nil, blockRuntime(fmt.Errorf("lookup persisted intent %s: %w", intent.ID, lookupErr))
		}
	}
	return ready, nil
}
