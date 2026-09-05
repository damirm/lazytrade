package sqlite_test

import (
	"context"
	"encoding/json"
	"errors"
	"testing"
	"time"

	"github.com/damirm/lazytrade/internal/storage"
)

func newBacktestRun(id string, started time.Time) storage.BacktestRun {
	return storage.BacktestRun{
		ID: id, ConfiguredRunID: "configured-" + id, StrategyID: "strategy-1",
		ApplicationVersion: "test", ConfigHash: "config", DatasetChecksum: "dataset",
		Seed: 42, StartedAt: started,
	}
}

func TestBacktestRunningToCompletedAndIdempotentReplay(t *testing.T) {
	store := openStore(t)
	ctx := context.Background()
	run, err := store.StartBacktestRun(ctx, newBacktestRun("run-completed", fixedTime))
	if err != nil {
		t.Fatal(err)
	}
	// Repeating the same semantic start is allowed.
	if _, err := store.StartBacktestRun(ctx, newBacktestRun("run-completed", fixedTime)); err != nil {
		t.Fatalf("repeat start: %v", err)
	}
	finished := fixedTime.Add(time.Hour)
	input := storage.FinishBacktestRun{
		RunID: run.ID, ExpectedRevision: 0, Status: storage.BacktestCompleted, FinishedAt: finished,
		Metrics: json.RawMessage(`{"return":"1.2"}`), Warnings: json.RawMessage(`[]`),
		Artifacts: []storage.BacktestArtifact{{
			ID: "artifact-report", ArtifactType: "report", Path: "report.json", Checksum: "sha256",
			SizeBytes: 123, SchemaVersion: 1, MediaType: "application/json", CreatedAt: finished,
		}},
	}
	got, err := store.FinishBacktestRun(ctx, input)
	if err != nil {
		t.Fatal(err)
	}
	if got.Status != storage.BacktestCompleted || got.Revision != 1 || len(got.Artifacts) != 1 {
		t.Fatalf("completed run=%+v", got)
	}
	// JSON whitespace is not semantically significant for an exact replay.
	input.Metrics = json.RawMessage("{ \"return\": \"1.2\" }")
	if _, err := store.FinishBacktestRun(ctx, input); err != nil {
		t.Fatalf("repeat finish: %v", err)
	}
}

func TestBacktestFailedAndCancelled(t *testing.T) {
	for _, status := range []storage.BacktestStatus{storage.BacktestFailed, storage.BacktestCancelled} {
		t.Run(string(status), func(t *testing.T) {
			store := openStore(t)
			run, err := store.StartBacktestRun(context.Background(), newBacktestRun("run-"+string(status), fixedTime))
			if err != nil {
				t.Fatal(err)
			}
			got, err := store.FinishBacktestRun(context.Background(), storage.FinishBacktestRun{
				RunID: run.ID, Status: status, FinishedAt: fixedTime.Add(time.Minute),
				ErrorCode: "stopped", ErrorMessage: "test",
			})
			if err != nil {
				t.Fatal(err)
			}
			if got.Status != status || got.Revision != 1 {
				t.Fatalf("terminal run=%+v", got)
			}
		})
	}
}

func TestBacktestInvalidTransitionAndVersion(t *testing.T) {
	store := openStore(t)
	ctx := context.Background()
	run, err := store.StartBacktestRun(ctx, newBacktestRun("run-transition", fixedTime))
	if err != nil {
		t.Fatal(err)
	}
	finish := storage.FinishBacktestRun{
		RunID: run.ID, ExpectedRevision: 0, Status: storage.BacktestFailed,
		FinishedAt: fixedTime.Add(time.Minute), ErrorCode: "failed",
	}
	if _, err := store.FinishBacktestRun(ctx, finish); err != nil {
		t.Fatal(err)
	}
	finish.Status = storage.BacktestCompleted
	if _, err := store.FinishBacktestRun(ctx, finish); !errors.Is(err, storage.ErrConflict) {
		t.Fatalf("invalid transition error=%v", err)
	}

	versioned, err := store.StartBacktestRun(ctx, newBacktestRun("run-version", fixedTime))
	if err != nil {
		t.Fatal(err)
	}
	finish.RunID, finish.ExpectedRevision = versioned.ID, 1
	if _, err := store.FinishBacktestRun(ctx, finish); !errors.Is(err, storage.ErrVersionConflict) {
		t.Fatalf("version error=%v", err)
	}
}

func TestBacktestArtifactFailureRollsBackTerminalStatus(t *testing.T) {
	store := openStore(t)
	ctx := context.Background()
	first, _ := store.StartBacktestRun(ctx, newBacktestRun("run-first", fixedTime))
	second, _ := store.StartBacktestRun(ctx, newBacktestRun("run-second", fixedTime.Add(time.Second)))
	artifact := storage.BacktestArtifact{
		ID: "shared-artifact-id", ArtifactType: "report", Path: "first.json", Checksum: "sum",
		SizeBytes: 1, SchemaVersion: 1, MediaType: "application/json", CreatedAt: fixedTime.Add(time.Minute),
	}
	if _, err := store.FinishBacktestRun(ctx, storage.FinishBacktestRun{
		RunID: first.ID, Status: storage.BacktestCompleted, FinishedAt: fixedTime.Add(time.Minute),
		Artifacts: []storage.BacktestArtifact{artifact},
	}); err != nil {
		t.Fatal(err)
	}
	artifact.Path = "second.json"
	if _, err := store.FinishBacktestRun(ctx, storage.FinishBacktestRun{
		RunID: second.ID, Status: storage.BacktestCompleted, FinishedAt: fixedTime.Add(2 * time.Minute),
		Artifacts: []storage.BacktestArtifact{artifact},
	}); !errors.Is(err, storage.ErrConflict) {
		t.Fatalf("artifact conflict error=%v", err)
	}
	got, err := store.GetBacktestRun(ctx, second.ID)
	if err != nil {
		t.Fatal(err)
	}
	if got.Status != storage.BacktestRunning || got.Revision != 0 || got.FinishedAt != nil {
		t.Fatalf("terminal update survived rollback: %+v", got)
	}
}

func TestBacktestListOrdering(t *testing.T) {
	store := openStore(t)
	ctx := context.Background()
	for _, item := range []struct {
		id string
		at time.Time
	}{{"b", fixedTime}, {"a", fixedTime}, {"c", fixedTime.Add(time.Second)}} {
		if _, err := store.StartBacktestRun(ctx, newBacktestRun(item.id, item.at)); err != nil {
			t.Fatal(err)
		}
	}
	runs, err := store.ListBacktestRuns(ctx, 10)
	if err != nil {
		t.Fatal(err)
	}
	if len(runs) != 3 || runs[0].ID != "a" || runs[1].ID != "b" || runs[2].ID != "c" {
		t.Fatalf("run order=%v", []string{runs[0].ID, runs[1].ID, runs[2].ID})
	}
}
