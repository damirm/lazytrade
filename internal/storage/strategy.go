package storage

import (
	"context"
	"encoding/json"
	"time"

	"github.com/damirm/lazytrade/internal/domain"
)

type StrategyRuntime struct {
	StrategyID    domain.StrategyID
	StateVersion  uint32
	StatePayload  json.RawMessage
	Revision      uint64
	EventCursor   domain.EventCursor
	StateChecksum string
	UpdatedAt     time.Time
}

type StrategyLifecycle struct {
	StrategyID domain.StrategyID
	Status     string
	Reason     string
	UpdatedAt  time.Time
}

type StrategyEventCommit struct {
	StrategyID      domain.StrategyID
	ExpectedVersion uint64
	StateVersion    uint32
	StatePayload    json.RawMessage
	EventCursor     domain.EventCursor
	StateChecksum   string
	Signals         []domain.Signal
	UpdatedAt       time.Time
}

type StrategyEventStore interface {
	RegisterStrategy(context.Context, StrategyDefinition) error
	LoadRuntime(context.Context, domain.StrategyID) (StrategyRuntime, error)
	CommitEvent(context.Context, StrategyEventCommit) error
}

type StrategyDefinition struct {
	ID                domain.StrategyID
	ExchangeAccountID domain.ExchangeAccountID
	InstrumentID      domain.InstrumentID
	StrategyType      string
	ConfigHash        string
	CreatedAt         time.Time
	UpdatedAt         time.Time
}
