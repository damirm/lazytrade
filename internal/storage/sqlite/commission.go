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

func (s *Store) ApplyCumulativeOrderCommission(
	ctx context.Context,
	accountID domain.ExchangeAccountID,
	exchangeOrderID domain.OrderID,
	cumulative domain.Money,
	observedAt time.Time,
	tradingDay string,
) (domain.Money, bool, error) {
	zero := domain.Money{Amount: decimal.Zero, Asset: cumulative.Asset}
	if err := accountID.Validate(); err != nil {
		return zero, false, fmt.Errorf("commission account: %w", err)
	}
	if err := exchangeOrderID.Validate(); err != nil {
		return zero, false, fmt.Errorf("commission order: %w", err)
	}
	if err := cumulative.Validate(); err != nil || cumulative.Amount.IsNegative() {
		return zero, false, errors.New("cumulative order commission must be non-negative with a valid asset")
	}
	observedMicros, err := micros(observedAt)
	if err != nil {
		return zero, false, fmt.Errorf("commission observed at: %w", err)
	}
	if _, err := time.Parse("2006-01-02", tradingDay); err != nil {
		return zero, false, errors.New("commission trading day must use YYYY-MM-DD")
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return zero, false, fmt.Errorf("sqlite: begin cumulative commission: %w", err)
	}
	defer tx.Rollback()
	q := generated.New(tx)
	order, err := q.FindOrderForCommission(ctx, generated.FindOrderForCommissionParams{
		ExchangeAccountID: string(accountID),
		ExchangeOrderID:   sql.NullString{String: string(exchangeOrderID), Valid: true},
	})
	if errors.Is(err, sql.ErrNoRows) {
		return zero, false, fmt.Errorf("exchange order %s: %w", exchangeOrderID, storage.ErrNotFound)
	}
	if err != nil {
		return zero, false, fmt.Errorf("sqlite: find commission order: %w", err)
	}
	delta, applied, err := advanceOrderCommissionState(ctx, q, order.ID, domain.StrategyID(order.StrategyID), cumulative, observedMicros)
	if err != nil {
		return zero, false, err
	}
	if applied {
		if err := applyCommissionDelta(ctx, q, domain.StrategyID(order.StrategyID), order.ID, cumulative, delta.Amount, tradingDay, observedMicros); err != nil {
			return zero, false, err
		}
	}
	if err := tx.Commit(); err != nil {
		return zero, false, fmt.Errorf("sqlite: commit cumulative commission: %w", err)
	}
	return delta, applied, nil
}

func advanceOrderCommissionState(
	ctx context.Context,
	q *generated.Queries,
	orderID string,
	strategyID domain.StrategyID,
	target domain.Money,
	observedMicros int64,
) (domain.Money, bool, error) {
	zero := domain.Money{Amount: decimal.Zero, Asset: target.Asset}
	row, err := q.GetOrderCommission(ctx, orderID)
	if errors.Is(err, sql.ErrNoRows) {
		baseline, baselineAsset, baselineObserved, err := persistedOrderCommission(ctx, q, orderID)
		if err != nil {
			return zero, false, err
		}
		if baselineAsset != "" && baselineAsset != target.Asset {
			return zero, false, fmt.Errorf("order commission asset changed from %s to %s: %w", baselineAsset, target.Asset, storage.ErrConflict)
		}
		if baselineAsset == "" {
			baselineAsset = target.Asset
		}
		if baselineObserved > observedMicros {
			observedMicros = baselineObserved
		}
		inserted, err := q.InsertOrderCommission(ctx, generated.InsertOrderCommissionParams{
			OrderID: orderID, StrategyID: string(strategyID), Asset: baselineAsset,
			CumulativeAmount: baseline.String(), ObservedAt: observedMicros,
		})
		if err != nil {
			return zero, false, fmt.Errorf("sqlite: initialize order commission: %w", err)
		}
		if inserted != 1 {
			return zero, false, fmt.Errorf("order commission %s changed concurrently: %w", orderID, storage.ErrConflict)
		}
		row = generated.OrderCommission{OrderID: orderID, StrategyID: string(strategyID), Asset: baselineAsset, CumulativeAmount: baseline.String(), ObservedAt: observedMicros}
	} else if err != nil {
		return zero, false, fmt.Errorf("sqlite: load order commission: %w", err)
	}
	if row.StrategyID != string(strategyID) {
		return zero, false, errors.New("sqlite: order commission belongs to another strategy")
	}
	if row.Asset != target.Asset {
		return zero, false, fmt.Errorf("order commission asset changed from %s to %s: %w", row.Asset, target.Asset, storage.ErrConflict)
	}
	current, err := decimal.NewFromString(row.CumulativeAmount)
	if err != nil {
		return zero, false, errors.New("sqlite: invalid cumulative order commission")
	}
	comparison := target.Amount.Cmp(current)
	if comparison < 0 {
		return zero, false, fmt.Errorf("order commission regressed from %s to %s: %w", current, target.Amount, storage.ErrConflict)
	}
	if comparison == 0 {
		return zero, false, nil
	}
	if row.ObservedAt > observedMicros {
		observedMicros = row.ObservedAt
	}
	affected, err := q.AdvanceOrderCommission(ctx, generated.AdvanceOrderCommissionParams{
		CumulativeAmount: target.Amount.String(), ObservedAt: observedMicros,
		OrderID: orderID, Asset: target.Asset, CumulativeAmount_2: current.String(),
	})
	if err != nil {
		return zero, false, fmt.Errorf("sqlite: advance order commission: %w", err)
	}
	if affected != 1 {
		return zero, false, fmt.Errorf("order commission %s changed concurrently: %w", orderID, storage.ErrConflict)
	}
	return domain.Money{Amount: target.Amount.Sub(current), Asset: target.Asset}, true, nil
}

func persistedOrderCommission(ctx context.Context, q *generated.Queries, orderID string) (decimal.Decimal, string, int64, error) {
	rows, err := q.ListPersistedCommissionsForOrder(ctx, orderID)
	if err != nil {
		return decimal.Zero, "", 0, fmt.Errorf("sqlite: list persisted order commissions: %w", err)
	}
	total, asset, observedAt := decimal.Zero, "", int64(0)
	for _, row := range rows {
		amount, err := decimal.NewFromString(row.CommissionAmount)
		if err != nil || amount.IsNegative() {
			return decimal.Zero, "", 0, errors.New("sqlite: invalid persisted order commission")
		}
		if asset != "" && asset != row.CommissionAsset {
			return decimal.Zero, "", 0, fmt.Errorf("persisted order commission asset conflict: %w", storage.ErrConflict)
		}
		asset = row.CommissionAsset
		total = total.Add(amount)
		if row.ReceivedAt > observedAt {
			observedAt = row.ReceivedAt
		}
	}
	return total, asset, observedAt, nil
}

func synchronizeExecutionCommissionState(ctx context.Context, q *generated.Queries, orderID string, strategyID domain.StrategyID, target domain.Money, observedAt int64) (domain.Money, error) {
	zero := domain.Money{Amount: decimal.Zero, Asset: target.Asset}
	row, err := q.GetOrderCommission(ctx, orderID)
	if err == nil {
		if row.StrategyID != string(strategyID) || row.Asset != target.Asset {
			return zero, fmt.Errorf("execution commission state conflict: %w", storage.ErrConflict)
		}
		current, parseErr := decimal.NewFromString(row.CumulativeAmount)
		if parseErr != nil {
			return zero, errors.New("sqlite: invalid cumulative order commission")
		}
		// History may already have observed a cumulative value which includes
		// this fill. Its later stream delivery must not charge it twice.
		if !target.Amount.GreaterThan(current) {
			return zero, nil
		}
	} else if !errors.Is(err, sql.ErrNoRows) {
		return zero, fmt.Errorf("sqlite: load execution commission state: %w", err)
	}
	delta, _, err := advanceOrderCommissionState(ctx, q, orderID, strategyID, target, observedAt)
	return delta, err
}

func applyCommissionDelta(ctx context.Context, q *generated.Queries, strategyID domain.StrategyID, orderID string, cumulative domain.Money, delta decimal.Decimal, tradingDay string, updatedAt int64) error {
	if !delta.IsPositive() {
		return errors.New("sqlite: commission delta must be positive")
	}
	row, err := q.GetDailyStatistics(ctx, generated.GetDailyStatisticsParams{
		StrategyID: string(strategyID), TradingDay: tradingDay, Asset: cumulative.Asset,
	})
	realized, unrealized, commissions, funding := decimal.Zero, decimal.Zero, decimal.Zero, decimal.Zero
	tradeCount, complete := int64(0), int64(0)
	if err == nil {
		values := []*decimal.Decimal{&realized, &unrealized, &commissions, &funding}
		for index, raw := range []string{row.RealizedPnl, row.UnrealizedPnl, row.Commissions, row.Funding} {
			*values[index], err = decimal.NewFromString(raw)
			if err != nil {
				return errors.New("sqlite: invalid persisted daily statistics")
			}
		}
		tradeCount, complete = row.TradeCount, row.Complete
	} else if !errors.Is(err, sql.ErrNoRows) {
		return fmt.Errorf("sqlite: load commission daily statistics: %w", err)
	}
	nextCommissions := commissions.Add(delta)
	if err := q.UpsertDailyStatistics(ctx, generated.UpsertDailyStatisticsParams{
		StrategyID: string(strategyID), TradingDay: tradingDay, Asset: cumulative.Asset,
		RealizedPnl: realized.String(), UnrealizedPnl: unrealized.String(),
		TotalPnl:    realized.Add(unrealized).Sub(nextCommissions).Add(funding).String(),
		Commissions: nextCommissions.String(), Funding: funding.String(),
		TradeCount: tradeCount, Complete: complete, UpdatedAt: updatedAt,
	}); err != nil {
		return fmt.Errorf("sqlite: update commission daily statistics: %w", err)
	}
	idSum := sha256.Sum256([]byte("order-commission/v1:" + orderID + ":" + cumulative.Asset + ":" + cumulative.Amount.String()))
	if err := q.InsertPnLEvent(ctx, generated.InsertPnLEventParams{
		ID: hex.EncodeToString(idSum[:]), StrategyID: string(strategyID), EventType: "order_commission_delta",
		Amount: delta.String(), Asset: cumulative.Asset, ComponentType: sql.NullString{String: "commission", Valid: true},
		OccurredAt: updatedAt,
	}); err != nil {
		return fmt.Errorf("sqlite: insert order commission P&L: %w", err)
	}
	return nil
}
