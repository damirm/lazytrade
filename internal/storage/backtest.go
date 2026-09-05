package storage

import (
	"context"
	"encoding/json"
	"time"
)

type BacktestStatus string

const (
	BacktestRunning   BacktestStatus = "running"
	BacktestCompleted BacktestStatus = "completed"
	BacktestFailed    BacktestStatus = "failed"
	BacktestCancelled BacktestStatus = "cancelled"
)

type BacktestRun struct {
	ID                 string
	ConfiguredRunID    string
	StrategyID         string
	ApplicationVersion string
	ConfigHash         string
	DatasetChecksum    string
	Seed               int64
	Status             BacktestStatus
	Revision           uint64
	StartedAt          time.Time
	FinishedAt         *time.Time
	Metrics            json.RawMessage
	Warnings           json.RawMessage
	ErrorCode          string
	ErrorMessage       string
	Artifacts          []BacktestArtifact
}

type BacktestArtifact struct {
	ID            string
	ArtifactType  string
	Path          string
	Checksum      string
	SizeBytes     int64
	SchemaVersion uint64
	MediaType     string
	CreatedAt     time.Time
}

type FinishBacktestRun struct {
	RunID            string
	ExpectedRevision uint64
	Status           BacktestStatus
	FinishedAt       time.Time
	Metrics          json.RawMessage
	Warnings         json.RawMessage
	ErrorCode        string
	ErrorMessage     string
	Artifacts        []BacktestArtifact
}

// BacktestStore persists run metadata. Terminal status and all artifact
// manifests are committed atomically.
type BacktestStore interface {
	StartBacktestRun(context.Context, BacktestRun) (BacktestRun, error)
	FinishBacktestRun(context.Context, FinishBacktestRun) (BacktestRun, error)
	GetBacktestRun(context.Context, string) (BacktestRun, error)
	ListBacktestRuns(context.Context, uint32) ([]BacktestRun, error)
}
