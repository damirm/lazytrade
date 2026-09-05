package sqlite

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"time"

	"github.com/damirm/lazytrade/internal/domain"
	"github.com/damirm/lazytrade/internal/storage"
	"github.com/damirm/lazytrade/internal/storage/sqlite/generated"
)

func (s *Store) RegisterStrategy(ctx context.Context, definition storage.StrategyDefinition) error {
	if definition.ID.Validate() != nil || definition.ExchangeAccountID.Validate() != nil ||
		definition.InstrumentID.Validate() != nil || definition.StrategyType == "" ||
		definition.ConfigHash == "" {
		return errors.New("sqlite: invalid strategy definition")
	}
	created, err := micros(definition.CreatedAt)
	if err != nil {
		return err
	}
	updated, err := micros(definition.UpdatedAt)
	if err != nil {
		return err
	}
	err = s.queries.InsertStrategyInstance(ctx, generated.InsertStrategyInstanceParams{
		ID: string(definition.ID), ExchangeAccountID: string(definition.ExchangeAccountID),
		InstrumentID: string(definition.InstrumentID), StrategyType: definition.StrategyType,
		ConfigHash: definition.ConfigHash, CreatedAt: created, UpdatedAt: updated,
	})
	if err == nil {
		return nil
	}
	var account, instrument, strategyType, configHash string
	scanErr := s.db.QueryRowContext(ctx, `SELECT exchange_account_id, instrument_id, strategy_type, config_hash
		FROM strategy_instances WHERE id=?`, definition.ID).
		Scan(&account, &instrument, &strategyType, &configHash)
	if scanErr == nil && account == string(definition.ExchangeAccountID) &&
		instrument == string(definition.InstrumentID) && strategyType == definition.StrategyType &&
		configHash == definition.ConfigHash {
		return nil
	}
	if scanErr == nil {
		return fmt.Errorf("strategy %s: %w", definition.ID, storage.ErrConflict)
	}
	return fmt.Errorf("sqlite: insert strategy %s: %w", definition.ID, storage.ErrConflict)
}

func (s *Store) SetStrategyStatus(
	ctx context.Context,
	strategyID domain.StrategyID,
	status string,
	reason string,
	updatedAt time.Time,
) error {
	if strategyID.Validate() != nil || status == "" {
		return errors.New("sqlite: invalid strategy lifecycle update")
	}
	updated, err := micros(updatedAt)
	if err != nil {
		return err
	}
	rows, err := s.queries.UpdateStrategyLifecycle(ctx, generated.UpdateStrategyLifecycleParams{
		RuntimeStatus: status,
		StatusReason:  reason,
		UpdatedAt:     updated,
		StrategyID:    string(strategyID),
	})
	if err != nil {
		return fmt.Errorf("sqlite: update strategy lifecycle %s: %w", strategyID, err)
	}
	if rows == 0 {
		return fmt.Errorf("strategy runtime %s: %w", strategyID, storage.ErrNotFound)
	}
	return nil
}

func (s *Store) LoadRuntime(ctx context.Context, id domain.StrategyID) (storage.StrategyRuntime, error) {
	row, err := s.queries.GetStrategyRuntime(ctx, string(id))
	if errors.Is(err, sql.ErrNoRows) {
		return storage.StrategyRuntime{}, fmt.Errorf("strategy runtime %s: %w", id, storage.ErrNotFound)
	}
	if err != nil {
		return storage.StrategyRuntime{}, fmt.Errorf("sqlite: load strategy runtime %s: %w", id, err)
	}
	if !json.Valid([]byte(row.StatePayload)) {
		return storage.StrategyRuntime{}, fmt.Errorf("sqlite: strategy runtime %s has invalid JSON", id)
	}
	return storage.StrategyRuntime{
		StrategyID: domain.StrategyID(row.StrategyID), StateVersion: uint32(row.StateVersion),
		StatePayload: json.RawMessage(row.StatePayload), Revision: uint64(row.Revision),
		RuntimeStatus: row.RuntimeStatus, StatusReason: row.StatusReason,
		EventCursor: domain.EventCursor{Timestamp: fromMicros(row.EventTimestamp),
			Priority: uint16(row.EventPriority), Sequence: uint64(row.EventSequence)},
		StateChecksum: row.StateChecksum, UpdatedAt: fromMicros(row.UpdatedAt),
	}, nil
}

func (s *Store) CommitEvent(ctx context.Context, commit storage.StrategyEventCommit) error {
	if commit.StrategyID.Validate() != nil || commit.StateVersion == 0 ||
		!json.Valid(commit.StatePayload) || commit.RuntimeStatus == "" || commit.StateChecksum == "" {
		return errors.New("sqlite: invalid strategy event commit")
	}
	updated, err := micros(commit.UpdatedAt)
	if err != nil {
		return err
	}
	cursorTime, err := micros(commit.EventCursor.Timestamp)
	if err != nil {
		return err
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return fmt.Errorf("sqlite: begin strategy event: %w", err)
	}
	defer tx.Rollback()
	q := generated.New(tx)

	current, loadErr := q.GetStrategyRuntime(ctx, string(commit.StrategyID))
	if loadErr != nil && !errors.Is(loadErr, sql.ErrNoRows) {
		return fmt.Errorf("sqlite: read strategy revision: %w", loadErr)
	}
	if loadErr == nil {
		sameCursor := current.EventTimestamp == cursorTime &&
			current.EventPriority == int64(commit.EventCursor.Priority) &&
			current.EventSequence == int64(commit.EventCursor.Sequence)
		if uint64(current.Revision) != commit.ExpectedVersion {
			if sameCursor && current.StateChecksum == commit.StateChecksum {
				return nil
			}
			return storage.ErrVersionConflict
		}
		if cursorLess(commit.EventCursor, domain.EventCursor{
			Timestamp: fromMicros(current.EventTimestamp),
			Priority:  uint16(current.EventPriority), Sequence: uint64(current.EventSequence),
		}) {
			return fmt.Errorf("strategy %s event cursor regressed: %w", commit.StrategyID, storage.ErrConflict)
		}
		rows, err := q.UpdateStrategyRuntime(ctx, generated.UpdateStrategyRuntimeParams{
			StateVersion: int64(commit.StateVersion), StatePayload: string(commit.StatePayload),
			RuntimeStatus: commit.RuntimeStatus, StatusReason: commit.StatusReason,
			EventTimestamp: cursorTime, EventPriority: int64(commit.EventCursor.Priority),
			EventSequence: int64(commit.EventCursor.Sequence), StateChecksum: commit.StateChecksum,
			UpdatedAt: updated, StrategyID: string(commit.StrategyID), Revision: int64(commit.ExpectedVersion),
		})
		if err != nil {
			return fmt.Errorf("sqlite: update strategy state: %w", err)
		}
		if rows != 1 {
			return storage.ErrVersionConflict
		}
	} else {
		if commit.ExpectedVersion != 0 {
			return storage.ErrVersionConflict
		}
		if err := q.InsertStrategyRuntime(ctx, generated.InsertStrategyRuntimeParams{
			StrategyID: string(commit.StrategyID), StateVersion: int64(commit.StateVersion),
			StatePayload: string(commit.StatePayload), RuntimeStatus: commit.RuntimeStatus,
			StatusReason: commit.StatusReason, EventTimestamp: cursorTime,
			EventPriority: int64(commit.EventCursor.Priority), EventSequence: int64(commit.EventCursor.Sequence),
			StateChecksum: commit.StateChecksum, UpdatedAt: updated,
		}); err != nil {
			return fmt.Errorf("sqlite: insert strategy state: %w", err)
		}
	}
	for _, signal := range commit.Signals {
		if signal.StrategyID != commit.StrategyID || signal.CausativeCursor != commit.EventCursor {
			return errors.New("sqlite: signal does not belong to strategy event")
		}
		if err := insertSignal(ctx, q, signal); err != nil {
			return err
		}
	}
	if err := tx.Commit(); err != nil {
		return fmt.Errorf("sqlite: commit strategy event: %w", err)
	}
	return nil
}

func insertSignal(ctx context.Context, q *generated.Queries, signal domain.Signal) error {
	if err := signal.Validate(); err != nil {
		return fmt.Errorf("sqlite: invalid signal: %w", err)
	}
	checksum, err := signalChecksum(signal)
	if err != nil {
		return err
	}
	created, _ := micros(signal.CreatedAt)
	cursorTime, _ := micros(signal.CausativeCursor.Timestamp)
	var limitPrice, priceAsset sql.NullString
	if signal.LimitPrice != nil {
		limitPrice = sql.NullString{String: signal.LimitPrice.Value.String(), Valid: true}
		priceAsset = sql.NullString{String: signal.LimitPrice.Asset, Valid: true}
	}
	err = q.InsertSignal(ctx, generated.InsertSignalParams{
		ID: string(signal.ID), StrategyID: string(signal.StrategyID),
		ExchangeAccountID: string(signal.ExchangeAccountID), InstrumentID: string(signal.InstrumentID),
		Action: int64(signal.Action), OrderType: int64(signal.OrderType),
		Quantity: signal.Quantity.Value.String(), LimitPrice: limitPrice, PriceAsset: priceAsset,
		ReasonCode: signal.ReasonCode, Reason: signal.Reason, CreatedAt: created,
		CursorTimestamp: cursorTime, CursorPriority: int64(signal.CausativeCursor.Priority),
		CursorSequence: int64(signal.CausativeCursor.Sequence), Ordinal: int64(signal.Ordinal),
		PayloadChecksum: checksum,
	})
	if err != nil {
		return fmt.Errorf("sqlite: insert signal %s: %w", signal.ID, storage.ErrConflict)
	}
	return nil
}

func cursorLess(left, right domain.EventCursor) bool {
	if !left.Timestamp.Equal(right.Timestamp) {
		return left.Timestamp.Before(right.Timestamp)
	}
	if left.Priority != right.Priority {
		return left.Priority < right.Priority
	}
	return left.Sequence < right.Sequence
}
