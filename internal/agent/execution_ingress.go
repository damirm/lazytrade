package agent

import (
	"context"
	"errors"
	"fmt"
	"sort"
	"sync"
	"time"

	"github.com/damirm/lazytrade/internal/domain"
	"github.com/damirm/lazytrade/internal/exchange"
	"github.com/damirm/lazytrade/internal/storage"
	"github.com/shopspring/decimal"
)

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
			case incoming, ok := <-stream.Executions:
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
				storeMu.Lock()
				execution, err := r.attributeExecution(ctx, accountID, incoming)
				if err == nil {
					tradingDay := r.executionTradingDay(execution.StrategyID, execution.ExecutedAt)
					_, _, err = r.Intents.StageExecution(ctx, accountID, execution, now, tradingDay)
				}
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
