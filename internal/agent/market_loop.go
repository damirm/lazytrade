package agent

import (
	"context"
	"errors"
	"fmt"

	"github.com/damirm/lazytrade/internal/domain"
	"github.com/damirm/lazytrade/internal/strategy"
)

func (r Runtime) runMarketLoop(ctx context.Context, state startupResult, failedStrategies map[domain.StrategyID]struct{}) error {
	accountID := state.accountID
	activeWorkers := state.activeWorkers
	risks := state.risks
	executionNotifications := state.executionNotifications
	executionPumpErrors := state.executionPumpErrors
	executionStoreMu := state.executionStoreMu
	pendingObservers := state.pendingObservers
	signalsRecovered := state.signalsRecovered
	events, streamErrors := state.marketStream.Events, state.marketStream.Errors
	for {
		select {
		case <-ctx.Done():
			return ctx.Err()
		case err, ok := <-streamErrors:
			if !ok {
				if err := ctx.Err(); err != nil {
					return err
				}
				return blockRuntime(errors.New("market data error stream closed"))
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
		case event, ok := <-events:
			if !ok {
				if err := ctx.Err(); err != nil {
					return err
				}
				// Both channels may become ready together. Preserve a buffered
				// terminal cause rather than replacing it with a generic EOF.
				select {
				case err := <-streamErrors:
					if err != nil {
						return blockRuntime(fmt.Errorf("market data stream: %w", err))
					}
				default:
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
					strategyID := r.strategyIDForInstrument(event.InstrumentID)
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
				strategyID := r.strategyIDForInstrument(event.InstrumentID)
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

func (r Runtime) riskForEvent(
	event domain.MarketEvent,
	workers map[domain.InstrumentID]*strategy.Worker,
	risks map[domain.StrategyID]SignalRisk,
) (SignalRisk, error) {
	if workers[event.InstrumentID] == nil {
		return nil, fmt.Errorf("market event for unconfigured instrument %q", event.InstrumentID)
	}
	strategyID := r.strategyIDForInstrument(event.InstrumentID)
	riskGate := risks[strategyID]
	if strategyID == "" || riskGate == nil {
		return nil, fmt.Errorf("risk routing for instrument %q is not configured", event.InstrumentID)
	}
	return riskGate, nil
}
