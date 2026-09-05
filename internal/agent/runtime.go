package agent

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"sort"
	"sync"
	"time"

	"github.com/damirm/lazytrade/internal/domain"
	"github.com/damirm/lazytrade/internal/exchange"
	"github.com/damirm/lazytrade/internal/storage"
	"github.com/damirm/lazytrade/internal/strategy"
	"github.com/shopspring/decimal"
)

type RiskDecision struct {
	Allowed    bool
	ReasonCode string
	Reason     string
}

type SignalRisk interface {
	Evaluate(context.Context, domain.Signal) (RiskDecision, error)
}

type MarketRiskObserver interface {
	ObserveMarket(context.Context, domain.MarketEvent) error
}

type Store interface {
	storage.IntentStore
	storage.SignalOutboxStore
	storage.OrderOutboxStore
	storage.ExecutionStore
	storage.ExecutionInboxStore
	storage.OrderCommissionStore
	storage.ExecutionHistoryCheckpointStore
}

var discardLogger = slog.New(slog.NewTextHandler(io.Discard, nil))

type Runtime struct {
	Logger                 *slog.Logger
	Exchange               exchange.Exchange
	Worker                 *strategy.Worker
	Workers                map[domain.InstrumentID]*strategy.Worker
	StrategyIDs            map[domain.InstrumentID]domain.StrategyID
	Risk                   SignalRisk
	Risks                  map[domain.StrategyID]SignalRisk
	Intents                Store
	Lifecycle              LifecycleStore
	Subscription           exchange.Subscription
	Subscriptions          []exchange.Subscription
	Ready                  chan<- struct{}
	OnOrder                func(domain.Order)
	OnStrategyError        func(domain.StrategyID, error)
	Now                    func() time.Time
	TradingDayKey          func(time.Time) string
	TradingDayKeys         map[domain.StrategyID]func(time.Time) string
	Reconciler             StartupReconciler
	HistorySource          string
	HistoryBootstrap       time.Duration
	HistoryOverlap         time.Duration
	HistoryVisibilityDelay time.Duration
}

func (r Runtime) Run(ctx context.Context) (resultErr error) {
	logger := r.logger()
	if r.Exchange == nil || r.Intents == nil {
		return errors.New("exchange and intent store are required")
	}
	workers, risks, subscriptions, err := r.components()
	if err != nil {
		return err
	}
	logger.InfoContext(ctx, "agent starting", "event", "agent_starting", "exchange", r.Exchange.Name(), "strategies", len(workers), "subscriptions", len(subscriptions))
	defer func() {
		if resultErr == nil || errors.Is(resultErr, context.Canceled) {
			logger.Info("agent stopped", "event", "agent_stopped")
			return
		}
		logger.Error("agent stopped with error", "event", "agent_failed", "error", resultErr)
	}()
	activeWorkers := make(map[domain.InstrumentID]*strategy.Worker, len(workers))
	for instrumentID, worker := range workers {
		activeWorkers[instrumentID] = worker
	}
	lifecycleIDs := r.lifecycleStrategyIDs()
	if r.Lifecycle != nil && len(lifecycleIDs) == 0 {
		return errors.New("strategy IDs are required for lifecycle persistence")
	}
	if err := r.repairFailedStrategySignals(ctx, lifecycleIDs); err != nil {
		return fmt.Errorf("repair failed strategy signals: %w", err)
	}
	if err := r.setLifecycle(ctx, lifecycleIDs, RuntimeStatusReconciling, "startup"); err != nil {
		return fmt.Errorf("persist reconciling lifecycle: %w", err)
	}
	failedStrategies := make(map[domain.StrategyID]struct{})
	if r.Lifecycle != nil {
		defer func() {
			terminalIDs := make([]domain.StrategyID, 0, len(lifecycleIDs))
			for _, strategyID := range lifecycleIDs {
				if _, failed := failedStrategies[strategyID]; !failed {
					terminalIDs = append(terminalIDs, strategyID)
				}
			}
			statusCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), 5*time.Second)
			defer cancel()
			if err := r.setTerminalLifecycle(statusCtx, terminalIDs, resultErr); err != nil {
				resultErr = errors.Join(resultErr, fmt.Errorf("persist terminal lifecycle: %w", err))
			}
		}()
	}
	accountID := domain.ExchangeAccountID(r.Exchange.Name())
	if err := accountID.Validate(); err != nil {
		return fmt.Errorf("agent exchange account: %w", err)
	}
	missingIntents, err := r.resolvePendingIntents(ctx)
	if err != nil {
		return fmt.Errorf("resolve pending intents: %w", err)
	}
	logger.InfoContext(ctx, "pending intents resolved", "event", "intent_recovery_completed", "ready", len(missingIntents))
	executionStream, err := r.Exchange.SubscribeExecutions(ctx, accountID)
	if err != nil {
		return blockRuntime(fmt.Errorf("subscribe executions: %w", err))
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
		return errors.New("history visibility delay must not be negative")
	}
	recoveryTo = recoveryTo.Add(-visibilityDelay)
	checkpoint, recoveredHistory, err := r.recoverExecutionHistory(ctx, accountID, recoveryTo, executionStoreMu)
	if err != nil {
		return blockRuntime(fmt.Errorf("recover execution history: %w", err))
	}
	logger.InfoContext(ctx, "execution history recovered", "event", "execution_history_recovered", "available", recoveredHistory)
	if err := r.drainPendingExecutionsSynchronized(ctx, accountID, executionStoreMu); err != nil {
		return blockRuntime(fmt.Errorf("drain pending executions: %w", err))
	}
	if r.Reconciler != nil {
		if _, err := r.Reconciler.Reconcile(ctx, accountID); err != nil {
			return blockRuntime(fmt.Errorf("startup reconciliation: %w", err))
		}
	}
	if recoveredHistory {
		if err := r.Intents.AdvanceExecutionHistoryCheckpoint(ctx, checkpoint); err != nil {
			return blockRuntime(fmt.Errorf("advance execution history checkpoint: %w", err))
		}
	}
	// Only ready intents, durably proven not to have crossed the API boundary,
	// may be submitted, and only after the execution stream has been opened.
	for _, intent := range missingIntents {
		if err := r.submitIntent(ctx, intent, requestForIntent(intent)); err != nil {
			return fmt.Errorf("submit pending intent %s: %w", intent.ID, err)
		}
	}
	// Close the snapshot/subscription gap before signals may produce orders.
	if r.Reconciler != nil {
		if _, err := r.Reconciler.Reconcile(ctx, accountID); err != nil {
			return blockRuntime(fmt.Errorf("post-subscription reconciliation: %w", err))
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
			return fmt.Errorf("recover pending signals: %w", err)
		}
		signalsRecovered = true
	}
	stream, err := r.Exchange.SubscribeMarketData(ctx, subscriptions)
	if err != nil {
		return blockRuntime(fmt.Errorf("subscribe market data: %w", err))
	}
	logger.InfoContext(ctx, "market data subscribed", "event", "market_data_subscribed", "subscriptions", len(subscriptions))
	if err := r.setLifecycle(ctx, lifecycleIDs, RuntimeStatusRunning, ""); err != nil {
		return fmt.Errorf("persist running lifecycle: %w", err)
	}
	logger.InfoContext(ctx, "agent running", "event", "agent_running", "strategies", len(workers))
	if r.Ready != nil {
		select {
		case r.Ready <- struct{}{}:
		case <-ctx.Done():
			return ctx.Err()
		}
	}
	events, streamErrors, states := stream.Events, stream.Errors, stream.State
	for {
		select {
		case <-ctx.Done():
			return ctx.Err()
		case err, ok := <-streamErrors:
			if !ok {
				streamErrors = nil
				continue
			}
			if err != nil {
				return blockRuntime(fmt.Errorf("market data stream: %w", err))
			}
		case err, ok := <-executionPumpErrors:
			if !ok {
				if ctxErr := ctx.Err(); ctxErr != nil {
					return ctxErr
				}
				return blockRuntime(errors.New("execution error stream closed"))
			}
			if err != nil {
				return blockRuntime(fmt.Errorf("execution stream: %w", err))
			}
		case _, ok := <-executionNotifications:
			if !ok {
				executionNotifications = nil
				continue
			}
			if err := r.drainPendingExecutionsSynchronized(ctx, accountID, executionStoreMu); err != nil {
				return fmt.Errorf("apply received executions: %w", err)
			}
		case state, ok := <-states:
			if !ok {
				states = nil
				continue
			}
			if state.State == exchange.StreamClosed {
				if err := ctx.Err(); err != nil {
					return err
				}
				return blockRuntime(errors.New("market data stream closed"))
			}
		case event, ok := <-events:
			if !ok {
				if err := ctx.Err(); err != nil {
					return err
				}
				return blockRuntime(errors.New("market data stream closed"))
			}
			if activeWorkers[event.InstrumentID] == nil {
				continue
			}
			eventRisk, riskErr := r.riskForEvent(event, activeWorkers, risks)
			if riskErr != nil {
				return riskErr
			}
			observer, observesMarket := eventRisk.(MarketRiskObserver)
			if observesMarket {
				if err := observer.ObserveMarket(ctx, event); err != nil {
					strategyID := r.StrategyIDs[event.InstrumentID]
					delete(pendingObservers, strategyID)
					strategyErr := strategyRuntimeError(strategyID, "risk market observation", err)
					isolated, isolateErr := r.isolateStrategyFailure(
						ctx, strategyErr, activeWorkers, failedStrategies,
					)
					if isolateErr != nil {
						return isolateErr
					}
					if isolated {
						continue
					}
					return strategyErr
				}
				strategyID := r.StrategyIDs[event.InstrumentID]
				if strategyID == "" && len(risks) == 1 {
					for id := range risks {
						strategyID = id
					}
				}
				delete(pendingObservers, strategyID)
			}
			if !signalsRecovered && len(pendingObservers) == 0 {
				if err := r.recoverSignalsWith(ctx, risks); err != nil {
					return fmt.Errorf("recover pending signals: %w", err)
				}
				signalsRecovered = true
			}
			if err := r.processEventWith(ctx, event, activeWorkers, risks); err != nil {
				isolated, isolateErr := r.isolateStrategyFailure(
					ctx, err, activeWorkers, failedStrategies,
				)
				if isolateErr != nil {
					return isolateErr
				}
				if isolated {
					continue
				}
				return err
			}
		}
	}
}

func (r Runtime) recordExecution(
	ctx context.Context,
	accountID domain.ExchangeAccountID,
	execution domain.Execution,
	receivedAt time.Time,
	tradingDay string,
) error {
	entry, _, err := r.Intents.StageExecution(ctx, accountID, execution, receivedAt, tradingDay)
	if err != nil {
		return fmt.Errorf("stage durable execution: %w", err)
	}
	if _, err := r.Intents.ApplyStagedExecution(ctx, entry.ID); err != nil {
		return fmt.Errorf("apply durable execution %s: %w", entry.ID, err)
	}
	return nil
}

func (r Runtime) startExecutionPump(
	ctx context.Context,
	accountID domain.ExchangeAccountID,
	stream exchange.ExecutionStream,
	storeMu *sync.Mutex,
) (<-chan struct{}, <-chan error) {
	notifications := make(chan struct{}, 1)
	errorsOut := make(chan error, 1)
	go func() {
		defer close(notifications)
		defer close(errorsOut)
		var lastReceived time.Time
		for {
			select {
			case <-ctx.Done():
				return
			case err, ok := <-stream.Errors:
				if !ok {
					if ctx.Err() == nil {
						errorsOut <- errors.New("execution error stream closed")
					}
					return
				}
				if err != nil {
					select {
					case errorsOut <- fmt.Errorf("execution stream: %w", err):
					case <-ctx.Done():
					}
					return
				}
			case execution, ok := <-stream.Executions:
				if !ok {
					if ctx.Err() == nil {
						errorsOut <- errors.New("execution stream closed")
					}
					return
				}
				now := time.Now().UTC()
				if r.Now != nil {
					now = r.Now().UTC()
				}
				if !now.After(lastReceived) {
					now = lastReceived.Add(time.Microsecond)
				}
				lastReceived = now
				tradingDay := r.executionTradingDay(execution.StrategyID, execution.ExecutedAt)
				storeMu.Lock()
				_, _, err := r.Intents.StageExecution(ctx, accountID, execution, now, tradingDay)
				storeMu.Unlock()
				if err != nil {
					select {
					case errorsOut <- fmt.Errorf("stage durable execution: %w", err):
					case <-ctx.Done():
					}
					return
				}
				r.logger().InfoContext(ctx, "execution received and staged", "event", "execution_staged", "execution_id", execution.ID, "order_id", execution.OrderID, "strategy_id", execution.StrategyID, "instrument_id", execution.InstrumentID, "quantity", execution.Quantity.Value.String(), "price", execution.Price.Value.String(), "price_asset", execution.Price.Asset)
				select {
				case notifications <- struct{}{}:
				default:
				}
			}
		}
	}()
	return notifications, errorsOut
}

func (r Runtime) recoverExecutionHistory(
	ctx context.Context,
	accountID domain.ExchangeAccountID,
	recoveryTo time.Time,
	storeMu *sync.Mutex,
) (storage.ExecutionHistoryCheckpoint, bool, error) {
	provider, ok := r.Exchange.(exchange.ExecutionHistoryProvider)
	if !ok {
		return storage.ExecutionHistoryCheckpoint{}, false, nil
	}
	source := r.HistorySource
	if source == "" {
		source = "exchange_operations"
	}
	bootstrap := r.HistoryBootstrap
	if bootstrap <= 0 {
		bootstrap = 7 * 24 * time.Hour
	}
	overlap := r.HistoryOverlap
	if overlap <= 0 {
		overlap = 24 * time.Hour
	}
	from := recoveryTo.Add(-bootstrap)
	checkpoint, err := r.Intents.LoadExecutionHistoryCheckpoint(ctx, accountID, source)
	if err == nil {
		from = checkpoint.CoveredThrough.Add(-overlap)
	} else if !errors.Is(err, storage.ErrNotFound) {
		return storage.ExecutionHistoryCheckpoint{}, false, err
	}
	if !from.Before(recoveryTo) {
		return storage.ExecutionHistoryCheckpoint{}, false, errors.New("execution history recovery window is empty")
	}
	history, err := provider.ExecutionHistory(ctx, exchange.ExecutionHistoryRequest{
		AccountID: accountID, From: from, To: recoveryTo,
	})
	if err != nil {
		return storage.ExecutionHistoryCheckpoint{}, false, err
	}
	if !history.Complete || !history.From.Equal(from) || !history.To.Equal(recoveryTo) {
		return storage.ExecutionHistoryCheckpoint{}, false, errors.New("execution history provider returned an incomplete or different window")
	}
	type attributedOrder struct {
		snapshot exchange.RecoveredOrderSnapshot
		intent   storage.OrderIntent
	}
	type attributedFill struct {
		order attributedOrder
		fill  exchange.RecoveredExecutionFill
	}
	orders := make([]attributedOrder, 0, len(history.Orders))
	var fills []attributedFill
	for _, snapshot := range history.Orders {
		intent, err := r.Intents.GetOrderIntentByClientOrderID(ctx, snapshot.ClientOrderID)
		if err != nil {
			return storage.ExecutionHistoryCheckpoint{}, false, fmt.Errorf("attribute history order %s: %w", snapshot.ExchangeOrderID, err)
		}
		if intent.ExchangeAccountID != accountID || intent.InstrumentID != snapshot.InstrumentID ||
			intent.Side != snapshot.Side || intent.OrderType != snapshot.OrderType ||
			!intent.Quantity.Value.Equal(snapshot.RequestedQuantity.Value) || intent.Status != "submitted" {
			return storage.ExecutionHistoryCheckpoint{}, false, fmt.Errorf("history order %s does not match submitted intent %s", snapshot.ExchangeOrderID, intent.ID)
		}
		order := attributedOrder{snapshot: snapshot, intent: intent}
		orders = append(orders, order)
		for _, fill := range snapshot.Fills {
			fills = append(fills, attributedFill{order: order, fill: fill})
		}
	}
	sort.Slice(fills, func(i, j int) bool {
		if fills[i].fill.ExecutedAt.Equal(fills[j].fill.ExecutedAt) {
			return fills[i].fill.TradeID < fills[j].fill.TradeID
		}
		return fills[i].fill.ExecutedAt.Before(fills[j].fill.ExecutedAt)
	})
	for index, recovered := range fills {
		snapshot, intent, fill := recovered.order.snapshot, recovered.order.intent, recovered.fill
		execution := domain.Execution{
			ID: domain.ExecutionID(fill.TradeID), OrderID: snapshot.ExchangeOrderID,
			StrategyID: intent.StrategyID, InstrumentID: intent.InstrumentID, Side: intent.Side,
			Quantity: fill.Quantity, Price: fill.Price,
			Commission: domain.Money{Amount: decimal.Zero, Asset: fill.Price.Asset},
			ExecutedAt: fill.ExecutedAt, ExchangeTrade: fill.TradeID,
		}
		tradingDay := r.executionTradingDay(intent.StrategyID, fill.ExecutedAt)
		storeMu.Lock()
		_, _, stageErr := r.Intents.StageExecution(
			ctx, accountID, execution, recoveryTo.Add(time.Duration(index)*time.Microsecond), tradingDay,
		)
		storeMu.Unlock()
		if stageErr != nil {
			return storage.ExecutionHistoryCheckpoint{}, false, fmt.Errorf("stage history trade %s: %w", fill.TradeID, stageErr)
		}
	}
	if err := r.drainPendingExecutionsSynchronized(ctx, accountID, storeMu); err != nil {
		return storage.ExecutionHistoryCheckpoint{}, false, err
	}
	for _, recovered := range orders {
		snapshot, intent := recovered.snapshot, recovered.intent
		if snapshot.CumulativeCommission.Amount.IsZero() {
			continue
		}
		if len(snapshot.Fills) == 0 {
			return storage.ExecutionHistoryCheckpoint{}, false, fmt.Errorf("history order %s has commission without fills", snapshot.ExchangeOrderID)
		}
		lastFill := snapshot.Fills[len(snapshot.Fills)-1]
		tradingDay := r.executionTradingDay(intent.StrategyID, lastFill.ExecutedAt)
		storeMu.Lock()
		_, _, commissionErr := r.Intents.ApplyCumulativeOrderCommission(
			ctx, accountID, snapshot.ExchangeOrderID, snapshot.CumulativeCommission, recoveryTo, tradingDay,
		)
		storeMu.Unlock()
		if commissionErr != nil {
			return storage.ExecutionHistoryCheckpoint{}, false, fmt.Errorf("apply history order %s commission: %w", snapshot.ExchangeOrderID, commissionErr)
		}
	}
	return storage.ExecutionHistoryCheckpoint{
		ExchangeAccountID: accountID, Source: source, CoveredThrough: recoveryTo, CreatedAt: recoveryTo,
	}, true, nil
}

func (r Runtime) drainPendingExecutionsSynchronized(
	ctx context.Context,
	accountID domain.ExchangeAccountID,
	storeMu *sync.Mutex,
) error {
	storeMu.Lock()
	defer storeMu.Unlock()
	return r.drainPendingExecutions(ctx, accountID)
}

func (r Runtime) executionTradingDay(strategyID domain.StrategyID, executedAt time.Time) string {
	tradingDay := executedAt.UTC().Format("2006-01-02")
	if dayKey := r.tradingDayKey(strategyID); dayKey != nil {
		tradingDay = dayKey(executedAt)
	}
	return tradingDay
}

func (r Runtime) drainPendingExecutions(ctx context.Context, accountID domain.ExchangeAccountID) error {
	const batchSize uint32 = 100
	for {
		entries, err := r.Intents.ListPendingExecutions(ctx, accountID, batchSize)
		if err != nil {
			return err
		}
		for _, entry := range entries {
			if _, err := r.Intents.ApplyStagedExecution(ctx, entry.ID); err != nil {
				return fmt.Errorf("apply pending execution %s: %w", entry.ID, err)
			}
		}
		if len(entries) < int(batchSize) {
			return nil
		}
	}
}

func (r Runtime) processEventWith(
	ctx context.Context,
	event domain.MarketEvent,
	workers map[domain.InstrumentID]*strategy.Worker,
	risks map[domain.StrategyID]SignalRisk,
) error {
	worker, ok := workers[event.InstrumentID]
	if !ok {
		return fmt.Errorf("market event for unconfigured instrument %q", event.InstrumentID)
	}
	signals, err := worker.Process(ctx, event)
	if err != nil {
		return strategyRuntimeError(r.StrategyIDs[event.InstrumentID], "worker", err)
	}
	for _, signal := range signals {
		if err := r.processSignalWith(ctx, signal, risks); err != nil {
			return err
		}
	}
	return nil
}

func (r Runtime) recoverSignalsWith(
	ctx context.Context,
	risks map[domain.StrategyID]SignalRisk,
) error {
	for {
		signals, err := r.Intents.ListSignalsPendingRisk(ctx, 100)
		if err != nil {
			return err
		}
		for _, signal := range signals {
			if err := r.processSignalWith(ctx, signal, risks); err != nil {
				return err
			}
		}
		if len(signals) < 100 {
			return nil
		}
	}
}

func (r Runtime) processSignalWith(
	ctx context.Context,
	signal domain.Signal,
	risks map[domain.StrategyID]SignalRisk,
) error {
	r.logger().InfoContext(ctx, "strategy signal received", "event", "signal_received", "signal_id", signal.ID, "strategy_id", signal.StrategyID, "instrument_id", signal.InstrumentID, "action", signal.Action, "reason_code", signal.ReasonCode)
	riskGate, ok := risks[signal.StrategyID]
	if !ok && len(risks) == 1 {
		for _, only := range risks {
			riskGate, ok = only, true
		}
	}
	if !ok {
		return fmt.Errorf("risk gate for strategy %q is not configured", signal.StrategyID)
	}
	decision, err := riskGate.Evaluate(ctx, signal)
	if err != nil {
		return fmt.Errorf("risk: %w", err)
	}
	storedDecision, decisionAudit, err := buildRiskDecision(signal, decision)
	if err != nil {
		return err
	}
	if !decision.Allowed {
		if err := r.Intents.RecordRiskDecision(ctx, storedDecision, decisionAudit); err != nil {
			return fmt.Errorf("persist risk decision: %w", err)
		}
		r.logger().WarnContext(ctx, "signal rejected by risk", "event", "risk_rejected", "signal_id", signal.ID, "strategy_id", signal.StrategyID, "reason_code", decision.ReasonCode)
		return nil
	}
	r.logger().InfoContext(ctx, "signal allowed by risk", "event", "risk_allowed", "signal_id", signal.ID, "strategy_id", signal.StrategyID)
	intent, intentAudit, orderRequest, err := buildIntent(signal)
	if err != nil {
		return err
	}
	if err := r.Intents.RecordAllowedDecisionIntent(ctx, storedDecision, intent, intentAudit); err != nil {
		return fmt.Errorf("persist allowed order intent: %w", err)
	}
	return r.submitIntent(ctx, intent, orderRequest)
}

func (r Runtime) components() (
	map[domain.InstrumentID]*strategy.Worker,
	map[domain.StrategyID]SignalRisk,
	[]exchange.Subscription,
	error,
) {
	workers := r.Workers
	if len(workers) == 0 && r.Worker != nil {
		workers = map[domain.InstrumentID]*strategy.Worker{
			r.Subscription.InstrumentID: r.Worker,
		}
	}
	risks := r.Risks
	if len(risks) == 0 && r.Risk != nil {
		risks = map[domain.StrategyID]SignalRisk{"": r.Risk}
	}
	subscriptions := r.Subscriptions
	if len(subscriptions) == 0 && r.Subscription.InstrumentID != "" {
		subscriptions = []exchange.Subscription{r.Subscription}
	}
	if len(workers) == 0 || len(risks) == 0 || len(subscriptions) == 0 {
		return nil, nil, nil, errors.New("at least one worker, risk gate, and subscription are required")
	}
	for instrumentID, worker := range workers {
		if instrumentID.Validate() != nil || worker == nil {
			return nil, nil, nil, fmt.Errorf("invalid worker for instrument %q", instrumentID)
		}
		if len(workers) > 1 {
			strategyID := r.StrategyIDs[instrumentID]
			if strategyID.Validate() != nil {
				return nil, nil, nil, fmt.Errorf("strategy ID for instrument %q is required", instrumentID)
			}
			if risks[strategyID] == nil {
				return nil, nil, nil, fmt.Errorf("risk gate for strategy %q is required", strategyID)
			}
		}
	}
	for i, subscription := range subscriptions {
		if err := subscription.Validate(); err != nil {
			return nil, nil, nil, fmt.Errorf("agent subscription %d: %w", i, err)
		}
		if workers[subscription.InstrumentID] == nil {
			return nil, nil, nil, fmt.Errorf("subscription %d has no worker", i)
		}
	}
	return workers, risks, subscriptions, nil
}

func (r Runtime) riskForEvent(
	event domain.MarketEvent,
	workers map[domain.InstrumentID]*strategy.Worker,
	risks map[domain.StrategyID]SignalRisk,
) (SignalRisk, error) {
	if workers[event.InstrumentID] == nil {
		return nil, fmt.Errorf("market event for unconfigured instrument %q", event.InstrumentID)
	}
	if len(risks) == 1 {
		for _, riskGate := range risks {
			return riskGate, nil
		}
	}
	strategyID := r.StrategyIDs[event.InstrumentID]
	riskGate := risks[strategyID]
	if strategyID == "" || riskGate == nil {
		return nil, fmt.Errorf("risk routing for instrument %q is not configured", event.InstrumentID)
	}
	return riskGate, nil
}

func (r Runtime) tradingDayKey(strategyID domain.StrategyID) func(time.Time) string {
	if key := r.TradingDayKeys[strategyID]; key != nil {
		return key
	}
	return r.TradingDayKey
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

func (r Runtime) submitIntent(ctx context.Context, intent storage.OrderIntent, request exchange.NewOrder) error {
	if intent.Status != "ready" {
		return fmt.Errorf("intent %s is %s, expected ready", intent.ID, intent.Status)
	}
	now := r.intentEventTime(intent)
	if err := r.Intents.TransitionOrderIntent(ctx, storage.IntentTransition{
		IntentID: intent.ID, FromStatus: "ready", ToStatus: "submitting",
		Audit: intentTransitionAudit(intent, "ready", "submitting", "submission_started", "", now),
	}); err != nil {
		return fmt.Errorf("begin order intent submission %s: %w", intent.ID, err)
	}
	intent.Status = "submitting"
	intent.UpdatedAt = now
	r.logger().InfoContext(ctx, "submitting order", "event", "order_submitting", "intent_id", intent.ID, "strategy_id", intent.StrategyID, "instrument_id", intent.InstrumentID, "side", intent.Side, "order_type", intent.OrderType, "quantity", intent.Quantity.Value.String())
	order, err := r.Exchange.PlaceOrder(ctx, request)
	if err == nil {
		if persistErr := r.recordSubmitted(ctx, intent, order, "placed"); persistErr != nil {
			return blockRuntime(persistErr)
		}
		return nil
	}
	var exchangeErr *exchange.Error
	if errors.As(err, &exchangeErr) && exchangeErr.Outcome == exchange.OutcomeUnknown {
		r.logger().ErrorContext(ctx, "order outcome unknown", "event", "order_outcome_unknown", "intent_id", intent.ID, "error", err)
		if persistErr := r.recordIntentWithoutOrder(ctx, intent, "unknown", "order_outcome_unknown", err.Error()); persistErr != nil {
			return errors.Join(err, persistErr)
		}
		return fmt.Errorf("place persisted intent %s has unknown outcome: %w", intent.ID, err)
	}
	if exchange.IsCategory(err, exchange.ErrorRejected) ||
		exchange.IsCategory(err, exchange.ErrorInvalidRequest) ||
		exchange.IsCategory(err, exchange.ErrorInsufficientFunds) ||
		exchange.IsCategory(err, exchange.ErrorPermission) ||
		exchange.IsCategory(err, exchange.ErrorAuthentication) {
		r.logger().WarnContext(ctx, "order rejected", "event", "order_rejected", "intent_id", intent.ID, "error", err)
		if persistErr := r.recordIntentWithoutOrder(ctx, intent, "rejected", "order_rejected", err.Error()); persistErr != nil {
			return errors.Join(err, persistErr)
		}
		return nil
	}
	if errors.As(err, &exchangeErr) && exchangeErr.Outcome == exchange.OutcomeKnownNotApplied &&
		(exchangeErr.Category == exchange.ErrorRateLimited || exchangeErr.Category == exchange.ErrorTransient) {
		now = r.intentEventTime(intent)
		transitionErr := r.Intents.TransitionOrderIntent(ctx, storage.IntentTransition{
			IntentID: intent.ID, FromStatus: "submitting", ToStatus: "ready",
			Audit: intentTransitionAudit(intent, "submitting", "ready", "submission_not_applied", err.Error(), now),
		})
		if transitionErr != nil {
			return errors.Join(err, transitionErr)
		}
		return fmt.Errorf("place persisted order intent %s was not applied: %w", intent.ID, err)
	}
	if !errors.As(err, &exchangeErr) {
		if persistErr := r.recordIntentWithoutOrder(ctx, intent, "unknown", "order_outcome_unclassified", err.Error()); persistErr != nil {
			return errors.Join(err, persistErr)
		}
		return fmt.Errorf("place persisted intent %s has unclassified outcome: %w", intent.ID, err)
	}
	return fmt.Errorf("place persisted order intent %s: %w", intent.ID, err)
}

func (r Runtime) recordSubmitted(ctx context.Context, intent storage.OrderIntent, order domain.Order, source string) error {
	if err := validateOrderForIntent(intent, order); err != nil {
		return fmt.Errorf("recovered order does not match intent: %w", err)
	}
	now := r.intentEventTime(intent)
	status := orderStatus(order.Status)
	recordID := sha256.Sum256([]byte("exchange-order/v1:" + intent.ID + ":" + string(order.ID)))
	record := storage.ExchangeOrder{
		ID: hex.EncodeToString(recordID[:]), OrderIntentID: intent.ID,
		ExchangeAccountID: order.ExchangeAccountID, ExchangeOrderID: order.ID,
		Status: status, RequestedQuantity: order.Quantity, FilledQuantity: order.FilledQuantity,
		SubmittedAt: order.SubmittedAt.UTC(), UpdatedAt: order.UpdatedAt.UTC(),
	}
	// The durable transition is a local observation. Exchange timestamps belong
	// to the order record and may be older or use a different clock.
	audit := resolutionAudit(intent, "submitted", source, now)
	if err := r.Intents.ResolveOrderIntent(ctx, storage.IntentResolution{
		IntentID: intent.ID, Status: "submitted", Order: &record, Audit: audit,
	}); err != nil {
		return fmt.Errorf("persist exchange order: %w", err)
	}
	if registrar, ok := r.Exchange.(exchange.OrderContextRegistrar); ok {
		registrar.RegisterOrderContext(order.ID, intent.StrategyID, intent.InstrumentID, intent.Side)
	}
	if r.OnOrder != nil {
		r.OnOrder(order)
	}
	r.logger().InfoContext(ctx, "order submitted", "event", "order_submitted", "intent_id", intent.ID, "order_id", order.ID, "strategy_id", intent.StrategyID, "instrument_id", intent.InstrumentID, "status", order.Status, "source", source)
	return nil
}

func (r Runtime) logger() *slog.Logger {
	if r.Logger != nil {
		return r.Logger
	}
	return discardLogger
}

func (r Runtime) recordIntentWithoutOrder(ctx context.Context, intent storage.OrderIntent, status, code, reason string) error {
	now := r.intentEventTime(intent)
	return r.Intents.ResolveOrderIntent(ctx, storage.IntentResolution{
		IntentID: intent.ID, Status: status,
		Audit: resolutionAudit(intent, status, code+":"+reason, now),
	})
}

func (r Runtime) intentEventTime(intent storage.OrderIntent) time.Time {
	now := time.Now().UTC()
	if r.Now != nil {
		now = r.Now().UTC()
	}
	// SQLite stores microseconds and ListAudit uses (created_at, id), so keep
	// consecutive transitions strictly ordered even with a fixed/coarse clock.
	if !now.After(intent.UpdatedAt) {
		return intent.UpdatedAt.Add(time.Microsecond)
	}
	return now
}

func validateOrderForIntent(intent storage.OrderIntent, order domain.Order) error {
	if order.ClientOrderID != intent.ClientOrderID ||
		order.ExchangeAccountID != intent.ExchangeAccountID ||
		order.InstrumentID != intent.InstrumentID || order.Side != intent.Side ||
		order.Type != intent.OrderType || !order.Quantity.Value.Equal(intent.Quantity.Value) {
		return errors.New("client ID, account, instrument, side, type, and quantity must match")
	}
	if (intent.LimitPrice == nil) != (order.LimitPrice == nil) {
		return errors.New("limit price presence does not match")
	}
	if intent.LimitPrice != nil &&
		(!intent.LimitPrice.Value.Equal(order.LimitPrice.Value) || intent.LimitPrice.Asset != order.LimitPrice.Asset) {
		return errors.New("limit price does not match")
	}
	return nil
}

func resolutionAudit(intent storage.OrderIntent, status, reason string, at time.Time) storage.AuditEvent {
	payload, _ := json.Marshal(struct {
		IntentID string `json:"intent_id"`
		Status   string `json:"status"`
		Reason   string `json:"reason"`
	}{intent.ID, status, reason})
	sum := sha256.Sum256([]byte("audit/order-resolution/v1:" + intent.ID + ":" + status))
	return storage.AuditEvent{
		ID: hex.EncodeToString(sum[:]), EventType: "order_intent_" + status,
		Actor: "agent", ScopeType: "strategy", ScopeID: string(intent.StrategyID),
		Payload: payload, CreatedAt: at,
	}
}

func intentTransitionAudit(
	intent storage.OrderIntent,
	from string,
	to string,
	code string,
	reason string,
	at time.Time,
) storage.AuditEvent {
	payload, _ := json.Marshal(struct {
		IntentID string `json:"intent_id"`
		From     string `json:"from"`
		To       string `json:"to"`
		Code     string `json:"code"`
		Reason   string `json:"reason,omitempty"`
	}{intent.ID, from, to, code, reason})
	sum := sha256.Sum256([]byte(fmt.Sprintf(
		"audit/order-transition/v1:%s:%s:%s:%d", intent.ID, from, to, at.UnixNano(),
	)))
	return storage.AuditEvent{
		ID: hex.EncodeToString(sum[:]), EventType: "order_intent_" + to,
		Actor: "agent", ScopeType: "strategy", ScopeID: string(intent.StrategyID),
		Payload: payload, CreatedAt: at,
	}
}

func requestForIntent(intent storage.OrderIntent) exchange.NewOrder {
	return exchange.NewOrder{
		ClientOrderID: intent.ClientOrderID, StrategyID: intent.StrategyID,
		ExchangeAccountID: intent.ExchangeAccountID, InstrumentID: intent.InstrumentID,
		Side: intent.Side, Type: intent.OrderType, Quantity: intent.Quantity,
		LimitPrice: intent.LimitPrice,
	}
}

func orderStatus(status domain.OrderStatus) string {
	switch status {
	case domain.OrderStatusPending:
		return "pending"
	case domain.OrderStatusAccepted:
		return "accepted"
	case domain.OrderStatusPartiallyFilled:
		return "partially_filled"
	case domain.OrderStatusFilled:
		return "filled"
	case domain.OrderStatusCancelled:
		return "cancelled"
	case domain.OrderStatusRejected:
		return "rejected"
	default:
		return "unknown"
	}
}

func buildRiskDecision(signal domain.Signal, decision RiskDecision) (storage.RiskDecision, storage.AuditEvent, error) {
	kind := "reject"
	reasonCode := decision.ReasonCode
	if decision.Allowed {
		kind = "allow"
		if reasonCode == "" {
			reasonCode = "allowed"
		}
	} else if reasonCode == "" {
		reasonCode = "rejected"
	}
	payload, err := json.Marshal(struct {
		SignalID   domain.SignalID `json:"signal_id"`
		Decision   string          `json:"decision"`
		ReasonCode string          `json:"reason_code"`
		Reason     string          `json:"reason"`
	}{signal.ID, kind, reasonCode, decision.Reason})
	if err != nil {
		return storage.RiskDecision{}, storage.AuditEvent{}, err
	}
	idSum := sha256.Sum256([]byte("risk-decision/v1:" + string(signal.ID)))
	id := hex.EncodeToString(idSum[:])
	record := storage.RiskDecision{
		ID: id, SignalID: signal.ID, Decision: kind, ReasonCode: reasonCode,
		Payload: payload, CreatedAt: signal.CreatedAt.UTC(),
	}
	auditSum := sha256.Sum256([]byte("audit/risk/v1:" + id))
	audit := storage.AuditEvent{
		ID: hex.EncodeToString(auditSum[:]), EventType: "risk_decision",
		Actor: "agent", ScopeType: "strategy", ScopeID: string(signal.StrategyID),
		Payload: payload, CreatedAt: signal.CreatedAt.UTC(),
	}
	return record, audit, nil
}

func buildIntent(signal domain.Signal) (storage.OrderIntent, storage.AuditEvent, exchange.NewOrder, error) {
	if err := signal.Validate(); err != nil {
		return storage.OrderIntent{}, storage.AuditEvent{}, exchange.NewOrder{}, err
	}
	side := domain.OrderSideBuy
	if signal.Action == domain.SignalSell || signal.Action == domain.SignalClose {
		side = domain.OrderSideSell
	}
	idSum := sha256.Sum256([]byte("intent/v1:" + string(signal.ID)))
	intentID := hex.EncodeToString(idSum[:])
	clientID := deterministicClientOrderID(signal.ID)
	payload, err := json.Marshal(struct {
		SignalID      domain.SignalID      `json:"signal_id"`
		ClientOrderID domain.ClientOrderID `json:"client_order_id"`
		Side          domain.OrderSide     `json:"side"`
		OrderType     domain.OrderType     `json:"order_type"`
		Quantity      string               `json:"quantity"`
	}{
		SignalID: signal.ID, ClientOrderID: clientID, Side: side,
		OrderType: signal.OrderType, Quantity: signal.Quantity.Value.String(),
	})
	if err != nil {
		return storage.OrderIntent{}, storage.AuditEvent{}, exchange.NewOrder{}, err
	}
	payloadSum := sha256.Sum256(payload)
	now := signal.CreatedAt.UTC()
	intent := storage.OrderIntent{
		ID: intentID, SignalID: signal.ID, StrategyID: signal.StrategyID,
		ExchangeAccountID: signal.ExchangeAccountID, InstrumentID: signal.InstrumentID,
		ClientOrderID: clientID, Side: side, OrderType: signal.OrderType,
		Quantity: signal.Quantity, LimitPrice: signal.LimitPrice, Status: "ready",
		PayloadChecksum: hex.EncodeToString(payloadSum[:]), CreatedAt: now, UpdatedAt: now,
	}
	auditIDSum := sha256.Sum256([]byte("audit/intent/v1:" + intentID))
	audit := storage.AuditEvent{
		ID: hex.EncodeToString(auditIDSum[:]), EventType: "order_intent_created",
		Actor: "agent", ScopeType: "strategy", ScopeID: string(signal.StrategyID),
		Payload: payload, CreatedAt: now,
	}
	request := exchange.NewOrder{
		ClientOrderID: clientID, StrategyID: signal.StrategyID,
		ExchangeAccountID: signal.ExchangeAccountID, InstrumentID: signal.InstrumentID,
		Side: side, Type: signal.OrderType, Quantity: signal.Quantity, LimitPrice: signal.LimitPrice,
	}
	return intent, audit, request, nil
}

func deterministicClientOrderID(signalID domain.SignalID) domain.ClientOrderID {
	sum := sha256.Sum256([]byte("client-order/v1:" + string(signalID)))
	// UUID-compatible deterministic identifier for exchange idempotency keys.
	sum[6] = (sum[6] & 0x0f) | 0x50
	sum[8] = (sum[8] & 0x3f) | 0x80
	return domain.ClientOrderID(fmt.Sprintf("%x-%x-%x-%x-%x", sum[0:4], sum[4:6], sum[6:8], sum[8:10], sum[10:16]))
}
