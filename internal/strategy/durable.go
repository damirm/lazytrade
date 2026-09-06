package strategy

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"sync"

	"github.com/damirm/lazytrade/internal/domain"
	"github.com/damirm/lazytrade/internal/storage"
)

// DurableStatePort adapts the atomic strategy event store to Worker StatePort.
// A Worker serializes calls, while the mutex also makes accidental concurrent
// use fail through optimistic revisions instead of losing updates.
type DurableStatePort struct {
	store        storage.StrategyEventStore
	strategyType string
	mu           sync.Mutex
	revisions    map[domain.StrategyID]uint64
}

func NewDurableStatePort(store storage.StrategyEventStore, strategyType string) (*DurableStatePort, error) {
	if store == nil || strategyType == "" {
		return nil, errors.New("strategy event store and strategy type are required")
	}
	return &DurableStatePort{
		store: store, strategyType: strategyType,
		revisions: make(map[domain.StrategyID]uint64),
	}, nil
}

func (p *DurableStatePort) Load(ctx context.Context, id domain.StrategyID) (Snapshot, bool, error) {
	p.mu.Lock()
	defer p.mu.Unlock()
	record, err := p.store.LoadRuntime(ctx, id)
	if errors.Is(err, storage.ErrNotFound) {
		p.revisions[id] = 0
		return Snapshot{}, false, nil
	}
	if err != nil {
		return Snapshot{}, false, err
	}
	p.revisions[id] = record.Revision
	cursor := record.EventCursor
	return Snapshot{
		State: StateEnvelope{
			StrategyType: p.strategyType, Version: record.StateVersion,
			Payload: append(json.RawMessage(nil), record.StatePayload...),
		},
		LastCursor: &cursor,
	}, true, nil
}

func (p *DurableStatePort) Commit(
	ctx context.Context,
	id domain.StrategyID,
	snapshot Snapshot,
	signals []domain.Signal,
) error {
	if snapshot.LastCursor == nil {
		return errors.New("durable strategy commit requires an event cursor")
	}
	if err := snapshot.State.Validate(p.strategyType); err != nil {
		return err
	}
	payload, err := json.Marshal(snapshot.State)
	if err != nil {
		return err
	}
	checksum := sha256.Sum256(payload)
	p.mu.Lock()
	defer p.mu.Unlock()
	revision := p.revisions[id]
	err = p.store.CommitEvent(ctx, storage.StrategyEventCommit{
		StrategyID: id, ExpectedVersion: revision,
		StateVersion:  snapshot.State.Version,
		StatePayload:  append(json.RawMessage(nil), snapshot.State.Payload...),
		EventCursor: *snapshot.LastCursor,
		StateChecksum: hex.EncodeToString(checksum[:]), Signals: signals,
		UpdatedAt: snapshot.LastCursor.Timestamp.UTC(),
	})
	if err != nil {
		return fmt.Errorf("commit durable strategy state: %w", err)
	}
	p.revisions[id] = revision + 1
	return nil
}

var _ StatePort = (*DurableStatePort)(nil)
