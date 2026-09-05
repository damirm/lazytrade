package agent

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"sync"

	"github.com/damirm/lazytrade/internal/domain"
	"github.com/damirm/lazytrade/internal/storage"
	"github.com/damirm/lazytrade/internal/strategy"
)

// PersistentStatePort adapts the durable strategy event store to the strategy
// worker. A worker serializes calls, while the mutex also makes accidental
// cross-goroutine use fail predictably.
type PersistentStatePort struct {
	mu           sync.Mutex
	store        storage.StrategyEventStore
	strategyType string
	revisions    map[domain.StrategyID]uint64
}

func NewPersistentStatePort(store storage.StrategyEventStore, strategyType string) (*PersistentStatePort, error) {
	if store == nil || strategyType == "" {
		return nil, errors.New("strategy event store and strategy type are required")
	}
	return &PersistentStatePort{store: store, strategyType: strategyType, revisions: make(map[domain.StrategyID]uint64)}, nil
}

func (p *PersistentStatePort) Load(ctx context.Context, id domain.StrategyID) (strategy.Snapshot, bool, error) {
	p.mu.Lock()
	defer p.mu.Unlock()
	runtime, err := p.store.LoadRuntime(ctx, id)
	if errors.Is(err, storage.ErrNotFound) {
		p.revisions[id] = 0
		return strategy.Snapshot{}, false, nil
	}
	if err != nil {
		return strategy.Snapshot{}, false, err
	}
	p.revisions[id] = runtime.Revision
	cursor := runtime.EventCursor
	return strategy.Snapshot{
		State: strategy.StateEnvelope{
			StrategyType: p.strategyType, Version: runtime.StateVersion,
			Payload: append([]byte(nil), runtime.StatePayload...),
		},
		LastCursor: &cursor,
	}, true, nil
}

func (p *PersistentStatePort) Commit(ctx context.Context, id domain.StrategyID, snapshot strategy.Snapshot, signals []domain.Signal) error {
	p.mu.Lock()
	defer p.mu.Unlock()
	if snapshot.LastCursor == nil {
		return errors.New("strategy snapshot cursor is required")
	}
	sum := sha256.Sum256(snapshot.State.Payload)
	err := p.store.CommitEvent(ctx, storage.StrategyEventCommit{
		StrategyID: id, ExpectedVersion: p.revisions[id],
		StateVersion: snapshot.State.Version, StatePayload: snapshot.State.Payload,
		RuntimeStatus: "running", EventCursor: *snapshot.LastCursor,
		StateChecksum: hex.EncodeToString(sum[:]), Signals: signals,
		UpdatedAt: snapshot.LastCursor.Timestamp.UTC(),
	})
	if err != nil {
		return err
	}
	p.revisions[id]++
	return nil
}

var _ strategy.StatePort = (*PersistentStatePort)(nil)
