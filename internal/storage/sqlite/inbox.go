package sqlite

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"time"

	"github.com/damirm/lazytrade/internal/domain"
	"github.com/damirm/lazytrade/internal/storage"
	"github.com/damirm/lazytrade/internal/storage/sqlite/generated"
)

const executionSourceFamily = "exchange"

func (s *Store) StageExecution(
	ctx context.Context,
	accountID domain.ExchangeAccountID,
	execution domain.Execution,
	receivedAt time.Time,
	tradingDay string,
) (storage.ExecutionInboxEntry, bool, error) {
	if err := accountID.Validate(); err != nil {
		return storage.ExecutionInboxEntry{}, false, fmt.Errorf("execution account: %w", err)
	}
	if err := execution.Validate(); err != nil {
		return storage.ExecutionInboxEntry{}, false, fmt.Errorf("execution: %w", err)
	}
	if _, err := time.Parse("2006-01-02", tradingDay); err != nil {
		return storage.ExecutionInboxEntry{}, false, errors.New("execution trading day must use YYYY-MM-DD")
	}
	receivedMicros, err := micros(receivedAt)
	if err != nil {
		return storage.ExecutionInboxEntry{}, false, fmt.Errorf("execution received at: %w", err)
	}
	payload, err := json.Marshal(execution)
	if err != nil {
		return storage.ExecutionInboxEntry{}, false, fmt.Errorf("sqlite: marshal inbox execution: %w", err)
	}
	checksum, err := executionIdentityChecksum(execution)
	if err != nil {
		return storage.ExecutionInboxEntry{}, false, err
	}
	dedupeKey := execution.ExchangeTrade
	if dedupeKey == "" {
		dedupeKey = string(execution.ID)
	}
	idSum := sha256.Sum256([]byte("execution-inbox/v1:" + string(accountID) + ":" + executionSourceFamily + ":" + dedupeKey))
	id := hex.EncodeToString(idSum[:])
	insertedRows, err := s.queries.InsertExecutionInbox(ctx, generated.InsertExecutionInboxParams{
		ID: id, ExchangeAccountID: string(accountID), SourceFamily: executionSourceFamily,
		DedupeKey: dedupeKey, PayloadChecksum: checksum, Payload: string(payload),
		TradingDay: tradingDay, ReceivedAt: receivedMicros,
	})
	if err != nil {
		return storage.ExecutionInboxEntry{}, false, fmt.Errorf("sqlite: stage execution: %w", err)
	}
	row, err := s.queries.GetExecutionInboxByDedupe(ctx, generated.GetExecutionInboxByDedupeParams{
		ExchangeAccountID: string(accountID), SourceFamily: executionSourceFamily, DedupeKey: dedupeKey,
	})
	if err != nil {
		return storage.ExecutionInboxEntry{}, false, fmt.Errorf("sqlite: read staged execution: %w", err)
	}
	if row.TradingDay != tradingDay {
		return storage.ExecutionInboxEntry{}, false, fmt.Errorf("execution dedupe key %q changed trading day: %w", dedupeKey, storage.ErrConflict)
	}
	entry, err := decodeExecutionInbox(row)
	if err != nil {
		return storage.ExecutionInboxEntry{}, false, err
	}
	if row.PayloadChecksum != checksum {
		// Version 4 checksums covered the full payload, including commission.
		// Re-evaluate the persisted immutable identity so upgrades and sources
		// with different commission allocation still deduplicate safely.
		existingIdentityChecksum, checksumErr := executionIdentityChecksum(entry.Execution)
		if checksumErr != nil {
			return storage.ExecutionInboxEntry{}, false, checksumErr
		}
		if existingIdentityChecksum != checksum {
			return storage.ExecutionInboxEntry{}, false, fmt.Errorf("execution dedupe key %q changed payload: %w", dedupeKey, storage.ErrConflict)
		}
	}
	return entry, insertedRows == 1, err
}

func executionIdentityChecksum(execution domain.Execution) (string, error) {
	// Commission is intentionally excluded: stream and history can report the
	// same immutable fill with different cumulative commission allocations.
	identity := struct {
		ID            domain.ExecutionID
		OrderID       domain.OrderID
		StrategyID    domain.StrategyID
		InstrumentID  domain.InstrumentID
		Side          domain.OrderSide
		Quantity      domain.Quantity
		Price         domain.Price
		ExecutedAt    time.Time
		ExchangeTrade string
	}{
		execution.ID, execution.OrderID, execution.StrategyID, execution.InstrumentID,
		execution.Side, execution.Quantity, execution.Price, execution.ExecutedAt, execution.ExchangeTrade,
	}
	payload, err := json.Marshal(identity)
	if err != nil {
		return "", fmt.Errorf("sqlite: marshal execution identity: %w", err)
	}
	sum := sha256.Sum256(payload)
	return hex.EncodeToString(sum[:]), nil
}

func (s *Store) ListPendingExecutions(ctx context.Context, accountID domain.ExchangeAccountID, limit uint32) ([]storage.ExecutionInboxEntry, error) {
	if err := accountID.Validate(); err != nil {
		return nil, fmt.Errorf("execution account: %w", err)
	}
	if limit == 0 {
		limit = 100
	}
	if limit > 1000 {
		return nil, errors.New("sqlite: pending execution limit exceeds 1000")
	}
	rows, err := s.queries.ListPendingExecutionInbox(ctx, generated.ListPendingExecutionInboxParams{
		ExchangeAccountID: string(accountID), Limit: int64(limit),
	})
	if err != nil {
		return nil, fmt.Errorf("sqlite: list pending executions: %w", err)
	}
	result := make([]storage.ExecutionInboxEntry, 0, len(rows))
	for _, row := range rows {
		entry, err := decodeExecutionInbox(row)
		if err != nil {
			return nil, err
		}
		result = append(result, entry)
	}
	return result, nil
}

func decodeExecutionInbox(row generated.ExecutionInbox) (storage.ExecutionInboxEntry, error) {
	var execution domain.Execution
	if err := json.Unmarshal([]byte(row.Payload), &execution); err != nil {
		return storage.ExecutionInboxEntry{}, fmt.Errorf("sqlite: decode inbox execution: %w", err)
	}
	if err := execution.Validate(); err != nil {
		return storage.ExecutionInboxEntry{}, fmt.Errorf("sqlite: invalid inbox execution: %w", err)
	}
	var appliedAt *time.Time
	if row.AppliedAt.Valid {
		value := fromMicros(row.AppliedAt.Int64)
		appliedAt = &value
	}
	return storage.ExecutionInboxEntry{
		ID: row.ID, ExchangeAccountID: domain.ExchangeAccountID(row.ExchangeAccountID),
		SourceFamily: row.SourceFamily, DedupeKey: row.DedupeKey, PayloadChecksum: row.PayloadChecksum,
		Execution: execution, TradingDay: row.TradingDay, Status: row.Status,
		ReceivedAt: fromMicros(row.ReceivedAt), AppliedAt: appliedAt,
	}, nil
}

func getExecutionInbox(ctx context.Context, q *generated.Queries, id string) (generated.ExecutionInbox, error) {
	row, err := q.GetExecutionInboxByID(ctx, id)
	if errors.Is(err, sql.ErrNoRows) {
		return generated.ExecutionInbox{}, fmt.Errorf("execution inbox %s: %w", id, storage.ErrNotFound)
	}
	if err != nil {
		return generated.ExecutionInbox{}, fmt.Errorf("sqlite: get execution inbox: %w", err)
	}
	return row, nil
}
