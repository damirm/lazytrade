package strategy

import (
	"context"
	"sync"

	"github.com/damirm/lazytrade/internal/domain"
)

type MemoryStatePort struct {
	mu      sync.Mutex
	records map[domain.StrategyID]Snapshot
	signals map[domain.StrategyID][]domain.Signal
}

func NewMemoryStatePort() *MemoryStatePort {
	return &MemoryStatePort{
		records: make(map[domain.StrategyID]Snapshot),
		signals: make(map[domain.StrategyID][]domain.Signal),
	}
}

func (m *MemoryStatePort) Load(_ context.Context, id domain.StrategyID) (Snapshot, bool, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	record, exists := m.records[id]
	return cloneSnapshot(record), exists, nil
}

func (m *MemoryStatePort) Commit(_ context.Context, id domain.StrategyID, snapshot Snapshot, signals []domain.Signal) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.records[id] = cloneSnapshot(snapshot)
	m.signals[id] = append(m.signals[id], signals...)
	return nil
}

func (m *MemoryStatePort) Signals(id domain.StrategyID) []domain.Signal {
	m.mu.Lock()
	defer m.mu.Unlock()
	return append([]domain.Signal(nil), m.signals[id]...)
}

func cloneSnapshot(input Snapshot) Snapshot {
	output := input
	output.State.Payload = append([]byte(nil), input.State.Payload...)
	if input.LastCursor != nil {
		cursor := *input.LastCursor
		output.LastCursor = &cursor
	}
	return output
}
