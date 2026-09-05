package sqlite

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"regexp"
	"time"

	"github.com/damirm/lazytrade/internal/domain"
	"github.com/damirm/lazytrade/internal/storage"
	"github.com/damirm/lazytrade/internal/storage/sqlite/generated"
)

const executionHistoryCheckpointPrefix = "execution_history_checkpoint/"

var historyCheckpointSourcePattern = regexp.MustCompile(`^[a-z0-9][a-z0-9._-]{0,63}$`)

type executionHistoryCheckpointPayload struct {
	Version        uint8  `json:"version"`
	Source         string `json:"source"`
	CoveredThrough string `json:"covered_through"`
}

func (s *Store) LoadExecutionHistoryCheckpoint(
	ctx context.Context,
	accountID domain.ExchangeAccountID,
	source string,
) (storage.ExecutionHistoryCheckpoint, error) {
	if err := validateHistoryCheckpointKey(accountID, source); err != nil {
		return storage.ExecutionHistoryCheckpoint{}, err
	}
	return loadExecutionHistoryCheckpoint(ctx, s.queries, accountID, source)
}

func loadExecutionHistoryCheckpoint(
	ctx context.Context,
	q *generated.Queries,
	accountID domain.ExchangeAccountID,
	source string,
) (storage.ExecutionHistoryCheckpoint, error) {
	row, err := q.GetLatestExecutionHistoryCheckpoint(ctx, generated.GetLatestExecutionHistoryCheckpointParams{
		EventType: checkpointEventType(source), ScopeID: string(accountID),
	})
	if errors.Is(err, sql.ErrNoRows) {
		return storage.ExecutionHistoryCheckpoint{}, fmt.Errorf("execution history checkpoint %s/%s: %w", accountID, source, storage.ErrNotFound)
	}
	if err != nil {
		return storage.ExecutionHistoryCheckpoint{}, fmt.Errorf("sqlite: load execution history checkpoint: %w", err)
	}
	var payload executionHistoryCheckpointPayload
	if err := json.Unmarshal([]byte(row.Payload), &payload); err != nil {
		return storage.ExecutionHistoryCheckpoint{}, fmt.Errorf("sqlite: decode execution history checkpoint: %w", err)
	}
	if payload.Version != 1 || payload.Source != source {
		return storage.ExecutionHistoryCheckpoint{}, errors.New("sqlite: invalid execution history checkpoint payload")
	}
	coveredThrough, err := time.Parse(time.RFC3339Nano, payload.CoveredThrough)
	if err != nil || coveredThrough.Location() != time.UTC {
		return storage.ExecutionHistoryCheckpoint{}, errors.New("sqlite: invalid execution history checkpoint watermark")
	}
	return storage.ExecutionHistoryCheckpoint{
		ExchangeAccountID: accountID, Source: source, CoveredThrough: coveredThrough,
		CreatedAt: fromMicros(row.CreatedAt),
	}, nil
}

func (s *Store) AdvanceExecutionHistoryCheckpoint(ctx context.Context, checkpoint storage.ExecutionHistoryCheckpoint) error {
	if err := validateHistoryCheckpoint(checkpoint); err != nil {
		return err
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return fmt.Errorf("sqlite: begin execution history checkpoint: %w", err)
	}
	defer tx.Rollback()
	q := generated.New(tx)
	eventCreatedAt := fromMicros(checkpoint.CreatedAt.UnixMicro())
	current, loadErr := loadExecutionHistoryCheckpoint(ctx, q, checkpoint.ExchangeAccountID, checkpoint.Source)
	if loadErr == nil {
		switch checkpoint.CoveredThrough.Compare(current.CoveredThrough) {
		case -1:
			return fmt.Errorf("execution history checkpoint regressed from %s to %s: %w", current.CoveredThrough, checkpoint.CoveredThrough, storage.ErrConflict)
		case 0:
			return nil
		}
		// Audit rows are ordered by creation time. Advance internal metadata if
		// two successful scans commit within the same database clock tick; the
		// public monotonic contract concerns CoveredThrough, not CreatedAt.
		if !eventCreatedAt.After(current.CreatedAt) {
			eventCreatedAt = current.CreatedAt.Add(time.Microsecond)
		}
	} else if !errors.Is(loadErr, storage.ErrNotFound) {
		return loadErr
	}
	payload, err := json.Marshal(executionHistoryCheckpointPayload{
		Version: 1, Source: checkpoint.Source,
		CoveredThrough: checkpoint.CoveredThrough.Format(time.RFC3339Nano),
	})
	if err != nil {
		return fmt.Errorf("sqlite: encode execution history checkpoint: %w", err)
	}
	idSum := sha256.Sum256([]byte("execution-history-checkpoint/v1:" +
		string(checkpoint.ExchangeAccountID) + ":" + checkpoint.Source + ":" +
		checkpoint.CoveredThrough.Format(time.RFC3339Nano)))
	event := storage.AuditEvent{
		ID: hex.EncodeToString(idSum[:]), EventType: checkpointEventType(checkpoint.Source),
		Actor: "execution_history_recovery", ScopeType: "exchange_account",
		ScopeID: string(checkpoint.ExchangeAccountID), Payload: payload, CreatedAt: eventCreatedAt,
	}
	if err := appendAudit(ctx, q, event); err != nil {
		return err
	}
	if err := tx.Commit(); err != nil {
		return fmt.Errorf("sqlite: commit execution history checkpoint: %w", err)
	}
	return nil
}

func validateHistoryCheckpoint(checkpoint storage.ExecutionHistoryCheckpoint) error {
	if err := validateHistoryCheckpointKey(checkpoint.ExchangeAccountID, checkpoint.Source); err != nil {
		return err
	}
	if checkpoint.CoveredThrough.IsZero() || checkpoint.CoveredThrough.Location() != time.UTC {
		return errors.New("sqlite: checkpoint covered through must be non-zero UTC")
	}
	if _, err := micros(checkpoint.CreatedAt); err != nil {
		return fmt.Errorf("sqlite: checkpoint created at: %w", err)
	}
	return nil
}

func validateHistoryCheckpointKey(accountID domain.ExchangeAccountID, source string) error {
	if err := accountID.Validate(); err != nil {
		return fmt.Errorf("execution history checkpoint account: %w", err)
	}
	if !historyCheckpointSourcePattern.MatchString(source) {
		return errors.New("sqlite: invalid execution history checkpoint source")
	}
	return nil
}

func checkpointEventType(source string) string { return executionHistoryCheckpointPrefix + source }
