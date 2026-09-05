package sqlite

import (
	"bytes"
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"strings"

	"github.com/damirm/lazytrade/internal/storage"
	"github.com/damirm/lazytrade/internal/storage/sqlite/generated"
)

func (s *Store) StartBacktestRun(ctx context.Context, run storage.BacktestRun) (storage.BacktestRun, error) {
	if err := validateStart(run); err != nil {
		return storage.BacktestRun{}, err
	}
	startedAt, _ := micros(run.StartedAt)
	err := s.queries.InsertBacktestRun(ctx, generated.InsertBacktestRunParams{
		ID: run.ID, ConfiguredRunID: run.ConfiguredRunID, StrategyID: run.StrategyID,
		ApplicationVersion: run.ApplicationVersion, ConfigHash: run.ConfigHash,
		DatasetChecksum: run.DatasetChecksum, Seed: run.Seed, StartedAt: startedAt,
	})
	if err == nil {
		run.Status, run.Revision = storage.BacktestRunning, 0
		return run, nil
	}
	existing, getErr := s.GetBacktestRun(ctx, run.ID)
	if getErr == nil && sameStartedRun(existing, run) {
		return existing, nil
	}
	if getErr == nil {
		return storage.BacktestRun{}, fmt.Errorf("backtest run %s: %w", run.ID, storage.ErrConflict)
	}
	return storage.BacktestRun{}, fmt.Errorf("sqlite: start backtest run %s: %w", run.ID, err)
}

func (s *Store) FinishBacktestRun(ctx context.Context, finish storage.FinishBacktestRun) (storage.BacktestRun, error) {
	if err := validateFinish(finish); err != nil {
		return storage.BacktestRun{}, err
	}
	finishedAt, _ := micros(finish.FinishedAt)
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return storage.BacktestRun{}, fmt.Errorf("sqlite: begin finish backtest: %w", err)
	}
	defer tx.Rollback()
	q := generated.New(tx)
	affected, err := q.FinishBacktestRun(ctx, generated.FinishBacktestRunParams{
		Status: string(finish.Status), FinishedAt: sql.NullInt64{Int64: finishedAt, Valid: true},
		MetricsPayload: jsonNullString(finish.Metrics), WarningsPayload: jsonNullString(finish.Warnings),
		ErrorCode: nullableString(finish.ErrorCode), ErrorMessage: nullableString(finish.ErrorMessage),
		ID: finish.RunID, Revision: int64(finish.ExpectedRevision),
	})
	if err != nil {
		return storage.BacktestRun{}, fmt.Errorf("sqlite: finish backtest run %s: %w", finish.RunID, err)
	}
	if affected == 0 {
		existing, getErr := getBacktestRun(ctx, q, finish.RunID)
		if getErr != nil {
			return storage.BacktestRun{}, getErr
		}
		if sameFinishedRun(existing, finish) {
			return existing, nil
		}
		if existing.Status != storage.BacktestRunning {
			return storage.BacktestRun{}, fmt.Errorf("backtest run %s transition %s -> %s: %w",
				finish.RunID, existing.Status, finish.Status, storage.ErrConflict)
		}
		return storage.BacktestRun{}, fmt.Errorf("backtest run %s revision: %w", finish.RunID, storage.ErrVersionConflict)
	}
	for _, artifact := range finish.Artifacts {
		createdAt, _ := micros(artifact.CreatedAt)
		err := q.InsertBacktestArtifact(ctx, generated.InsertBacktestArtifactParams{
			ID: artifact.ID, BacktestRunID: finish.RunID, ArtifactType: artifact.ArtifactType,
			Path: artifact.Path, ContentChecksum: artifact.Checksum, Size: artifact.SizeBytes,
			SchemaVersion: int64(artifact.SchemaVersion), MediaType: artifact.MediaType, CreatedAt: createdAt,
		})
		if err != nil {
			return storage.BacktestRun{}, fmt.Errorf("sqlite: insert backtest artifact %s: %w", artifact.ID, storage.ErrConflict)
		}
	}
	if err := tx.Commit(); err != nil {
		return storage.BacktestRun{}, fmt.Errorf("sqlite: commit finish backtest: %w", err)
	}
	return s.GetBacktestRun(ctx, finish.RunID)
}

func (s *Store) GetBacktestRun(ctx context.Context, id string) (storage.BacktestRun, error) {
	return getBacktestRun(ctx, s.queries, id)
}

func getBacktestRun(ctx context.Context, q *generated.Queries, id string) (storage.BacktestRun, error) {
	row, err := q.GetBacktestRun(ctx, id)
	if errors.Is(err, sql.ErrNoRows) {
		return storage.BacktestRun{}, fmt.Errorf("backtest run %s: %w", id, storage.ErrNotFound)
	}
	if err != nil {
		return storage.BacktestRun{}, fmt.Errorf("sqlite: get backtest run %s: %w", id, err)
	}
	artifacts, err := q.ListBacktestArtifacts(ctx, id)
	if err != nil {
		return storage.BacktestRun{}, fmt.Errorf("sqlite: list backtest artifacts: %w", err)
	}
	return mapBacktestRun(row, artifacts)
}

func (s *Store) ListBacktestRuns(ctx context.Context, limit uint32) ([]storage.BacktestRun, error) {
	if limit == 0 {
		limit = 100
	}
	if limit > 1000 {
		return nil, errors.New("sqlite: backtest run limit exceeds 1000")
	}
	rows, err := s.queries.ListBacktestRuns(ctx, int64(limit))
	if err != nil {
		return nil, fmt.Errorf("sqlite: list backtest runs: %w", err)
	}
	result := make([]storage.BacktestRun, 0, len(rows))
	for _, row := range rows {
		artifacts, err := s.queries.ListBacktestArtifacts(ctx, row.ID)
		if err != nil {
			return nil, fmt.Errorf("sqlite: list backtest artifacts: %w", err)
		}
		run, err := mapBacktestRun(row, artifacts)
		if err != nil {
			return nil, err
		}
		result = append(result, run)
	}
	return result, nil
}

func validateStart(run storage.BacktestRun) error {
	if strings.TrimSpace(run.ID) == "" || strings.TrimSpace(run.ConfiguredRunID) == "" ||
		strings.TrimSpace(run.StrategyID) == "" || strings.TrimSpace(run.ApplicationVersion) == "" ||
		strings.TrimSpace(run.ConfigHash) == "" || strings.TrimSpace(run.DatasetChecksum) == "" {
		return errors.New("sqlite: invalid backtest run identity")
	}
	if (run.Status != "" && run.Status != storage.BacktestRunning) || run.Revision != 0 ||
		run.FinishedAt != nil || len(run.Metrics) != 0 || len(run.Warnings) != 0 ||
		run.ErrorCode != "" || run.ErrorMessage != "" || len(run.Artifacts) != 0 {
		return errors.New("sqlite: new backtest run contains terminal state")
	}
	if _, err := micros(run.StartedAt); err != nil {
		return fmt.Errorf("backtest started at: %w", err)
	}
	return nil
}

func validateFinish(f storage.FinishBacktestRun) error {
	if f.RunID == "" || (f.Status != storage.BacktestCompleted && f.Status != storage.BacktestFailed &&
		f.Status != storage.BacktestCancelled) {
		return errors.New("sqlite: invalid terminal backtest status")
	}
	if _, err := micros(f.FinishedAt); err != nil {
		return fmt.Errorf("backtest finished at: %w", err)
	}
	if !validOptionalJSON(f.Metrics) || !validOptionalJSON(f.Warnings) {
		return errors.New("sqlite: invalid backtest JSON payload")
	}
	seenIDs, seenPaths := map[string]struct{}{}, map[string]struct{}{}
	for _, a := range f.Artifacts {
		if a.ID == "" || a.ArtifactType == "" || a.Path == "" || a.Checksum == "" ||
			a.MediaType == "" || a.SizeBytes < 0 || a.SchemaVersion == 0 {
			return errors.New("sqlite: invalid backtest artifact")
		}
		if _, err := micros(a.CreatedAt); err != nil {
			return fmt.Errorf("backtest artifact created at: %w", err)
		}
		key := a.ArtifactType + "\x00" + a.Path
		if _, exists := seenIDs[a.ID]; exists {
			return errors.New("sqlite: duplicate backtest artifact ID")
		}
		if _, exists := seenPaths[key]; exists {
			return errors.New("sqlite: duplicate backtest artifact path")
		}
		seenIDs[a.ID], seenPaths[key] = struct{}{}, struct{}{}
	}
	return nil
}

func validOptionalJSON(value json.RawMessage) bool { return len(value) == 0 || json.Valid(value) }

func jsonNullString(value json.RawMessage) sql.NullString {
	return sql.NullString{String: string(value), Valid: len(value) != 0}
}

func nullableString(value string) sql.NullString {
	return sql.NullString{String: value, Valid: value != ""}
}

func mapBacktestRun(row generated.BacktestRun, rows []generated.ListBacktestArtifactsRow) (storage.BacktestRun, error) {
	run := storage.BacktestRun{
		ID: row.ID, ConfiguredRunID: row.ConfiguredRunID, StrategyID: row.StrategyID,
		ApplicationVersion: row.ApplicationVersion, ConfigHash: row.ConfigHash,
		DatasetChecksum: row.DatasetChecksum, Seed: row.Seed, Status: storage.BacktestStatus(row.Status),
		Revision: uint64(row.Revision), StartedAt: fromMicros(row.StartedAt),
		Metrics: json.RawMessage(row.MetricsPayload.String), Warnings: json.RawMessage(row.WarningsPayload.String),
		ErrorCode: row.ErrorCode.String, ErrorMessage: row.ErrorMessage.String,
		Artifacts: make([]storage.BacktestArtifact, 0, len(rows)),
	}
	if row.FinishedAt.Valid {
		value := fromMicros(row.FinishedAt.Int64)
		run.FinishedAt = &value
	}
	for _, a := range rows {
		run.Artifacts = append(run.Artifacts, storage.BacktestArtifact{
			ID: a.ID, ArtifactType: a.ArtifactType, Path: a.Path, Checksum: a.ContentChecksum,
			SizeBytes: a.Size, SchemaVersion: uint64(a.SchemaVersion), MediaType: a.MediaType,
			CreatedAt: fromMicros(a.CreatedAt),
		})
	}
	return run, nil
}

func sameStartedRun(existing, candidate storage.BacktestRun) bool {
	return existing.ID == candidate.ID && existing.ConfiguredRunID == candidate.ConfiguredRunID &&
		existing.StrategyID == candidate.StrategyID && existing.ApplicationVersion == candidate.ApplicationVersion &&
		existing.ConfigHash == candidate.ConfigHash && existing.DatasetChecksum == candidate.DatasetChecksum &&
		existing.Seed == candidate.Seed && existing.StartedAt.Equal(candidate.StartedAt)
}

func sameFinishedRun(existing storage.BacktestRun, candidate storage.FinishBacktestRun) bool {
	if existing.Status != candidate.Status || existing.FinishedAt == nil ||
		!existing.FinishedAt.Equal(candidate.FinishedAt) || existing.ErrorCode != candidate.ErrorCode ||
		existing.ErrorMessage != candidate.ErrorMessage || !sameJSON(existing.Metrics, candidate.Metrics) ||
		!sameJSON(existing.Warnings, candidate.Warnings) || len(existing.Artifacts) != len(candidate.Artifacts) {
		return false
	}
	byID := make(map[string]storage.BacktestArtifact, len(existing.Artifacts))
	for _, artifact := range existing.Artifacts {
		byID[artifact.ID] = artifact
	}
	for _, artifact := range candidate.Artifacts {
		if existingArtifact, ok := byID[artifact.ID]; !ok || existingArtifact != artifact {
			return false
		}
	}
	return true
}

func sameJSON(left, right json.RawMessage) bool {
	if len(left) == 0 || len(right) == 0 {
		return len(left) == len(right)
	}
	var l, r any
	return json.Unmarshal(left, &l) == nil && json.Unmarshal(right, &r) == nil &&
		bytes.Equal(mustJSON(l), mustJSON(r))
}

func mustJSON(value any) []byte {
	result, _ := json.Marshal(value)
	return result
}
