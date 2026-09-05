package sqlite

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"errors"
	"fmt"
	"time"

	"github.com/damirm/lazytrade/internal/domain"
	"github.com/damirm/lazytrade/internal/storage"
	"github.com/damirm/lazytrade/internal/storage/sqlite/generated"
	"github.com/shopspring/decimal"
)

func (s *Store) ApplyStagedExecution(ctx context.Context, inboxID string) (bool, error) {
	if inboxID == "" {
		return false, errors.New("sqlite: execution inbox ID is empty")
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return false, fmt.Errorf("sqlite: begin execution: %w", err)
	}
	defer tx.Rollback()
	q := generated.New(tx)
	row, err := getExecutionInbox(ctx, q, inboxID)
	if err != nil {
		return false, err
	}
	if row.Status == "applied" {
		return false, nil
	}
	if row.Status != "pending" {
		return false, fmt.Errorf("sqlite: invalid execution inbox status %q", row.Status)
	}
	entry, err := decodeExecutionInbox(row)
	if err != nil {
		return false, err
	}
	receivedMicros := row.ReceivedAt
	applied, err := applyExecutionProjection(ctx, q, entry, receivedMicros)
	if err != nil {
		return false, err
	}
	affected, err := q.MarkExecutionInboxApplied(ctx, generated.MarkExecutionInboxAppliedParams{
		AppliedAt: sql.NullInt64{Int64: time.Now().UTC().UnixMicro(), Valid: true}, ID: inboxID,
	})
	if err != nil {
		return false, fmt.Errorf("sqlite: mark execution inbox applied: %w", err)
	}
	if affected != 1 {
		return false, fmt.Errorf("execution inbox %s changed concurrently: %w", inboxID, storage.ErrConflict)
	}
	if err := tx.Commit(); err != nil {
		return false, fmt.Errorf("sqlite: commit execution: %w", err)
	}
	return applied, nil
}

func applyExecutionProjection(ctx context.Context, q *generated.Queries, entry storage.ExecutionInboxEntry, receivedMicros int64) (bool, error) {
	accountID, execution := entry.ExchangeAccountID, entry.Execution
	dedupeKey, checksum := entry.DedupeKey, entry.PayloadChecksum
	existingChecksum, existingErr := q.GetExecutionChecksum(ctx, generated.GetExecutionChecksumParams{
		ExchangeAccountID: string(accountID), SourceFamily: entry.SourceFamily, DedupeKey: dedupeKey,
	})
	if existingErr == nil {
		if existingChecksum != checksum {
			return false, fmt.Errorf("execution dedupe key %q changed payload: %w", dedupeKey, storage.ErrConflict)
		}
		return false, nil
	}
	if !errors.Is(existingErr, sql.ErrNoRows) {
		return false, fmt.Errorf("sqlite: check duplicate execution: %w", existingErr)
	}
	order, err := q.FindOrderForExecution(ctx, generated.FindOrderForExecutionParams{
		ExchangeAccountID: string(accountID),
		ExchangeOrderID:   sql.NullString{String: string(execution.OrderID), Valid: true},
	})
	if errors.Is(err, sql.ErrNoRows) {
		return false, fmt.Errorf("exchange order %s: %w", execution.OrderID, storage.ErrNotFound)
	}
	if err != nil {
		return false, fmt.Errorf("sqlite: find execution order: %w", err)
	}
	if domain.StrategyID(order.StrategyID) != execution.StrategyID ||
		domain.InstrumentID(order.InstrumentID) != execution.InstrumentID ||
		domain.OrderSide(order.Side) != execution.Side {
		return false, errors.New("sqlite: execution does not match persisted order")
	}
	requested, err := decimal.NewFromString(order.RequestedQuantity)
	if err != nil {
		return false, errors.New("sqlite: invalid requested order quantity")
	}
	filled, err := decimal.NewFromString(order.FilledQuantity)
	if err != nil {
		return false, errors.New("sqlite: invalid filled order quantity")
	}
	// Establish the cumulative baseline before inserting this fill. For a
	// migrated database, persisted executions in that baseline have already
	// affected P&L; for a new order it is zero.
	baselineAmount, baselineAsset, baselineObservedAt, err := persistedOrderCommission(ctx, q, order.ID)
	if err != nil {
		return false, err
	}
	if baselineAsset == "" {
		baselineAsset = execution.Commission.Asset
	}
	if baselineObservedAt == 0 {
		baselineObservedAt = receivedMicros
	}
	if _, err := synchronizeExecutionCommissionState(ctx, q, order.ID, execution.StrategyID, domain.Money{
		Amount: baselineAmount, Asset: baselineAsset,
	}, baselineObservedAt); err != nil {
		return false, err
	}
	affected, err := q.InsertOrderExecution(ctx, generated.InsertOrderExecutionParams{
		ID: string(execution.ID), ExchangeAccountID: string(accountID),
		SourceFamily: entry.SourceFamily, DedupeKey: dedupeKey, PayloadChecksum: checksum,
		OrderID: order.ID, StrategyID: string(execution.StrategyID),
		InstrumentID: string(execution.InstrumentID), Quantity: execution.Quantity.Value.String(),
		Price: execution.Price.Value.String(), PriceAsset: execution.Price.Asset,
		CommissionAmount: execution.Commission.Amount.String(), CommissionAsset: execution.Commission.Asset,
		ExecutedAt: execution.ExecutedAt.UnixMicro(), ReceivedAt: receivedMicros,
	})
	if err != nil {
		return false, fmt.Errorf("sqlite: insert execution: %w", err)
	}
	if affected == 0 {
		existing, err := q.GetExecutionChecksum(ctx, generated.GetExecutionChecksumParams{
			ExchangeAccountID: string(accountID), SourceFamily: entry.SourceFamily, DedupeKey: dedupeKey,
		})
		if err != nil {
			return false, fmt.Errorf("sqlite: read duplicate execution: %w", err)
		}
		if existing != checksum {
			return false, fmt.Errorf("execution dedupe key %q changed payload: %w", dedupeKey, storage.ErrConflict)
		}
		return false, nil
	}
	quantities, err := q.ListExecutionQuantitiesForOrder(ctx, order.ID)
	if err != nil {
		return false, fmt.Errorf("sqlite: sum order executions: %w", err)
	}
	executedQuantity := decimal.Zero
	for _, raw := range quantities {
		value, err := decimal.NewFromString(raw)
		if err != nil {
			return false, errors.New("sqlite: invalid persisted execution quantity")
		}
		executedQuantity = executedQuantity.Add(value)
	}
	nextFilled := decimal.Max(filled, executedQuantity)
	if nextFilled.GreaterThan(requested) {
		return false, errors.New("sqlite: executions exceed requested order quantity")
	}
	status := "partially_filled"
	if nextFilled.Equal(requested) {
		status = "filled"
	}
	rows, err := q.UpdateOrderAfterExecution(ctx, generated.UpdateOrderAfterExecutionParams{
		FilledQuantity: nextFilled.String(), Status: status,
		UpdatedAt: receivedMicros, ID: order.ID,
	})
	if err != nil {
		return false, fmt.Errorf("sqlite: update order after execution: %w", err)
	}
	if rows != 1 {
		return false, errors.New("sqlite: execution order update affected no rows")
	}
	realized, closedTrade, err := applyPosition(ctx, q, execution, receivedMicros)
	if err != nil {
		return false, err
	}
	cumulativeAmount, commissionAsset, commissionObservedAt, err := persistedOrderCommission(ctx, q, order.ID)
	if err != nil {
		return false, err
	}
	commissionDelta, err := synchronizeExecutionCommissionState(ctx, q, order.ID, execution.StrategyID, domain.Money{
		Amount: cumulativeAmount, Asset: commissionAsset,
	}, commissionObservedAt)
	if err != nil {
		return false, err
	}
	if err := applyExecutionPnL(ctx, q, execution, commissionDelta.Amount, entry.TradingDay, realized, closedTrade, receivedMicros); err != nil {
		return false, err
	}
	return true, nil
}

func applyPosition(ctx context.Context, q *generated.Queries, execution domain.Execution, updatedAt int64) (decimal.Decimal, bool, error) {
	row, err := q.GetPosition(ctx, generated.GetPositionParams{
		StrategyID: string(execution.StrategyID), InstrumentID: string(execution.InstrumentID),
	})
	position, average := decimal.Zero, execution.Price.Value
	if err == nil {
		position, err = decimal.NewFromString(row.Quantity)
		if err != nil {
			return decimal.Zero, false, errors.New("sqlite: invalid persisted position quantity")
		}
		average, err = decimal.NewFromString(row.AveragePrice)
		if err != nil {
			return decimal.Zero, false, errors.New("sqlite: invalid persisted average price")
		}
		if row.ValuationAsset != execution.Price.Asset {
			return decimal.Zero, false, domain.ErrAssetMismatch
		}
	} else if !errors.Is(err, sql.ErrNoRows) {
		return decimal.Zero, false, fmt.Errorf("sqlite: load position: %w", err)
	}
	next, nextAverage := position, average
	realized, closedTrade := decimal.Zero, false
	if execution.Side == domain.OrderSideBuy {
		next = position.Add(execution.Quantity.Value)
		nextAverage = average
		if position.IsZero() {
			nextAverage = execution.Price.Value
		} else {
			nextAverage = average.Mul(position).Add(execution.Price.Value.Mul(execution.Quantity.Value)).Div(next)
		}
	} else {
		if execution.Quantity.Value.GreaterThan(position) {
			return decimal.Zero, false, errors.New("sqlite: sell execution exceeds long position")
		}
		next = position.Sub(execution.Quantity.Value)
		realized = execution.Price.Value.Sub(average).Mul(execution.Quantity.Value)
		closedTrade = true
	}
	if err := q.UpsertPosition(ctx, generated.UpsertPositionParams{
		StrategyID: string(execution.StrategyID), InstrumentID: string(execution.InstrumentID),
		Quantity: next.String(), AveragePrice: nextAverage.String(),
		ValuationAsset: execution.Price.Asset, UpdatedAt: updatedAt,
	}); err != nil {
		return decimal.Zero, false, fmt.Errorf("sqlite: update position: %w", err)
	}
	return realized, closedTrade, nil
}

func applyExecutionPnL(ctx context.Context, q *generated.Queries, execution domain.Execution, commissionDelta decimal.Decimal, tradingDay string, realized decimal.Decimal, closedTrade bool, updatedAt int64) error {
	currentRealized, currentUnrealized := decimal.Zero, decimal.Zero
	currentCommissions, currentFunding := decimal.Zero, decimal.Zero
	var tradeCount int64
	row, err := q.GetDailyStatistics(ctx, generated.GetDailyStatisticsParams{
		StrategyID: string(execution.StrategyID), TradingDay: tradingDay, Asset: execution.Price.Asset,
	})
	if err == nil {
		values := []*decimal.Decimal{&currentRealized, &currentUnrealized, &currentCommissions, &currentFunding}
		raw := []string{row.RealizedPnl, row.UnrealizedPnl, row.Commissions, row.Funding}
		for index := range raw {
			*values[index], err = decimal.NewFromString(raw[index])
			if err != nil {
				return errors.New("sqlite: invalid persisted daily statistics")
			}
		}
		tradeCount = row.TradeCount
	} else if !errors.Is(err, sql.ErrNoRows) {
		return fmt.Errorf("sqlite: load daily statistics: %w", err)
	}
	nextRealized := currentRealized.Add(realized)
	nextCommissions := currentCommissions.Add(commissionDelta)
	if closedTrade {
		tradeCount++
		if err := insertPnLComponent(ctx, q, execution, "realized", realized); err != nil {
			return err
		}
	}
	if !commissionDelta.IsZero() {
		if err := insertPnLComponent(ctx, q, execution, "commission", commissionDelta); err != nil {
			return err
		}
	}
	total := nextRealized.Add(currentUnrealized).Sub(nextCommissions).Add(currentFunding)
	if err := q.UpsertDailyStatistics(ctx, generated.UpsertDailyStatisticsParams{
		StrategyID: string(execution.StrategyID), TradingDay: tradingDay, Asset: execution.Price.Asset,
		RealizedPnl: nextRealized.String(), UnrealizedPnl: currentUnrealized.String(),
		TotalPnl: total.String(), Commissions: nextCommissions.String(),
		Funding: currentFunding.String(), TradeCount: tradeCount, Complete: 0, UpdatedAt: updatedAt,
	}); err != nil {
		return fmt.Errorf("sqlite: update daily statistics: %w", err)
	}
	return nil
}

func insertPnLComponent(ctx context.Context, q *generated.Queries, execution domain.Execution, component string, amount decimal.Decimal) error {
	sum := sha256.Sum256([]byte("pnl/v1:" + string(execution.ID) + ":" + component))
	if err := q.InsertPnLEvent(ctx, generated.InsertPnLEventParams{
		ID: hex.EncodeToString(sum[:]), StrategyID: string(execution.StrategyID),
		EventType: "execution_" + component, Amount: amount.String(), Asset: execution.Price.Asset,
		SourceExecutionID: sql.NullString{String: string(execution.ID), Valid: true},
		ComponentType:     sql.NullString{String: component, Valid: true},
		OccurredAt:        execution.ExecutedAt.UnixMicro(),
	}); err != nil {
		return fmt.Errorf("sqlite: insert P&L component: %w", err)
	}
	return nil
}

func (s *Store) LoadPosition(ctx context.Context, strategyID domain.StrategyID, instrumentID domain.InstrumentID) (storage.Position, error) {
	row, err := s.queries.GetPosition(ctx, generated.GetPositionParams{
		StrategyID: string(strategyID), InstrumentID: string(instrumentID),
	})
	if errors.Is(err, sql.ErrNoRows) {
		return storage.Position{}, fmt.Errorf("position %s/%s: %w", strategyID, instrumentID, storage.ErrNotFound)
	}
	if err != nil {
		return storage.Position{}, fmt.Errorf("sqlite: load position: %w", err)
	}
	quantity, err := domain.NewQuantity(row.Quantity)
	if err != nil {
		return storage.Position{}, err
	}
	average, err := domain.NewPrice(row.AveragePrice, row.ValuationAsset)
	if err != nil {
		return storage.Position{}, err
	}
	return storage.Position{
		StrategyID: domain.StrategyID(row.StrategyID), InstrumentID: domain.InstrumentID(row.InstrumentID),
		Quantity: quantity, AveragePrice: average, Revision: uint64(row.Revision),
		UpdatedAt: fromMicros(row.UpdatedAt),
	}, nil
}

func (s *Store) LoadDailyStatistics(ctx context.Context, strategyID domain.StrategyID, tradingDay, asset string) (storage.DailyStatistics, error) {
	row, err := s.queries.GetDailyStatistics(ctx, generated.GetDailyStatisticsParams{
		StrategyID: string(strategyID), TradingDay: tradingDay, Asset: asset,
	})
	if errors.Is(err, sql.ErrNoRows) {
		return storage.DailyStatistics{}, fmt.Errorf("daily statistics %s/%s/%s: %w", strategyID, tradingDay, asset, storage.ErrNotFound)
	}
	if err != nil {
		return storage.DailyStatistics{}, fmt.Errorf("sqlite: load daily statistics: %w", err)
	}
	values := make([]decimal.Decimal, 5)
	for index, raw := range []string{row.RealizedPnl, row.UnrealizedPnl, row.TotalPnl, row.Commissions, row.Funding} {
		values[index], err = decimal.NewFromString(raw)
		if err != nil {
			return storage.DailyStatistics{}, errors.New("sqlite: invalid daily statistics")
		}
	}
	return storage.DailyStatistics{
		StrategyID: domain.StrategyID(row.StrategyID), TradingDay: row.TradingDay, Asset: row.Asset,
		RealizedPnL: values[0], UnrealizedPnL: values[1], TotalPnL: values[2],
		Commissions: values[3], Funding: values[4], TradeCount: uint64(row.TradeCount),
		Complete: row.Complete == 1, UpdatedAt: fromMicros(row.UpdatedAt),
	}, nil
}

func (s *Store) ListPositionsByExchange(ctx context.Context, accountID domain.ExchangeAccountID) ([]storage.Position, error) {
	rows, err := s.queries.ListPositionsByExchange(ctx, string(accountID))
	if err != nil {
		return nil, fmt.Errorf("sqlite: list exchange positions: %w", err)
	}
	result := make([]storage.Position, 0, len(rows))
	for _, row := range rows {
		quantity, err := domain.NewQuantity(row.Quantity)
		if err != nil {
			return nil, err
		}
		average, err := domain.NewPrice(row.AveragePrice, row.ValuationAsset)
		if err != nil {
			return nil, err
		}
		result = append(result, storage.Position{
			StrategyID: domain.StrategyID(row.StrategyID), InstrumentID: domain.InstrumentID(row.InstrumentID),
			Quantity: quantity, AveragePrice: average, Revision: uint64(row.Revision),
			UpdatedAt: fromMicros(row.UpdatedAt),
		})
	}
	return result, nil
}

func (s *Store) ListOpenOrdersByExchange(ctx context.Context, accountID domain.ExchangeAccountID) ([]storage.LocalOpenOrder, error) {
	rows, err := s.queries.ListOpenOrdersByExchange(ctx, string(accountID))
	if err != nil {
		return nil, fmt.Errorf("sqlite: list local open orders: %w", err)
	}
	result := make([]storage.LocalOpenOrder, 0, len(rows))
	for _, row := range rows {
		if !row.ExchangeOrderID.Valid {
			return nil, errors.New("sqlite: open order has no exchange order ID")
		}
		requested, err := domain.NewQuantity(row.RequestedQuantity)
		if err != nil {
			return nil, err
		}
		filled, err := domain.NewQuantity(row.FilledQuantity)
		if err != nil {
			return nil, err
		}
		result = append(result, storage.LocalOpenOrder{
			StrategyID: domain.StrategyID(row.StrategyID), InstrumentID: domain.InstrumentID(row.InstrumentID),
			ClientOrderID: domain.ClientOrderID(row.ClientOrderID), ExchangeOrderID: domain.OrderID(row.ExchangeOrderID.String),
			Side: domain.OrderSide(row.Side), Status: row.Status, RequestedQuantity: requested, FilledQuantity: filled,
		})
	}
	return result, nil
}
