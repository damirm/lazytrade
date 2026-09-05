package strategy

import (
	"context"
	"crypto/sha256"
	"encoding/binary"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"sync"
	"time"

	"github.com/damirm/lazytrade/internal/domain"
)

const SignalIDAlgorithm = "signal/v1"

var ErrOutOfOrderEvent = errors.New("market event cursor is older than committed cursor")

type Snapshot struct {
	State      StateEnvelope
	LastCursor *domain.EventCursor
}

type StatePort interface {
	Load(context.Context, domain.StrategyID) (Snapshot, bool, error)
	Commit(context.Context, domain.StrategyID, Snapshot, []domain.Signal) error
}

type Worker struct {
	mu         sync.Mutex
	strategyID domain.StrategyID
	accountID  domain.ExchangeAccountID
	instrument domain.InstrumentID
	strategy   Strategy
	state      StatePort
}

func NewWorker(strategyID domain.StrategyID, accountID domain.ExchangeAccountID, instrument domain.InstrumentID, implementation Strategy, state StatePort) (*Worker, error) {
	if err := strategyID.Validate(); err != nil {
		return nil, fmt.Errorf("strategy ID: %w", err)
	}
	if err := accountID.Validate(); err != nil {
		return nil, fmt.Errorf("exchange account ID: %w", err)
	}
	if err := instrument.Validate(); err != nil {
		return nil, fmt.Errorf("instrument ID: %w", err)
	}
	if implementation == nil || state == nil {
		return nil, errors.New("strategy and state port are required")
	}
	return &Worker{strategyID: strategyID, accountID: accountID, instrument: instrument, strategy: implementation, state: state}, nil
}

// Process serializes all events for this worker. A cursor equal to the last
// committed cursor is a retry and returns no signals. Older cursors fail safe.
func (w *Worker) Process(ctx context.Context, event domain.MarketEvent) ([]domain.Signal, error) {
	w.mu.Lock()
	defer w.mu.Unlock()

	if err := event.Validate(); err != nil {
		return nil, fmt.Errorf("validate market event: %w", err)
	}
	if event.ExchangeAccountID != w.accountID || event.InstrumentID != w.instrument {
		return nil, errors.New("market event does not belong to worker")
	}
	cursor := CursorForEvent(event)
	snapshot, exists, err := w.state.Load(ctx, w.strategyID)
	if err != nil {
		return nil, fmt.Errorf("load strategy state: %w", err)
	}
	if exists && snapshot.LastCursor != nil {
		comparison := CompareCursors(cursor, *snapshot.LastCursor)
		if comparison == 0 {
			return nil, nil
		}
		if comparison < 0 {
			return nil, ErrOutOfOrderEvent
		}
	}
	current := snapshot.State
	if !exists {
		current, err = w.strategy.InitialState()
		if err != nil {
			return nil, fmt.Errorf("initial strategy state: %w", err)
		}
	}
	if err := current.Validate(w.strategy.Type()); err != nil {
		return nil, err
	}
	result, err := w.strategy.OnEvent(ctx, current, Input{
		StrategyID: w.strategyID, ExchangeAccount: w.accountID,
		InstrumentID: w.instrument, Event: event,
	})
	if err != nil {
		return nil, fmt.Errorf("process strategy event: %w", err)
	}
	if err := result.State.Validate(w.strategy.Type()); err != nil {
		return nil, err
	}
	if len(result.Signals) > int(^uint16(0)) {
		return nil, errors.New("strategy returned too many signals")
	}
	signals := make([]domain.Signal, len(result.Signals))
	for index, draft := range result.Signals {
		if err := ValidateDraft(draft); err != nil {
			return nil, fmt.Errorf("signal draft %d: %w", index, err)
		}
		ordinal := uint16(index)
		id, err := deterministicSignalID(w.strategyID, cursor, ordinal, draft)
		if err != nil {
			return nil, fmt.Errorf("signal draft %d ID: %w", index, err)
		}
		signals[index] = domain.Signal{
			ID: id, StrategyID: w.strategyID, ExchangeAccountID: w.accountID,
			InstrumentID: w.instrument, Action: draft.Action, OrderType: draft.OrderType,
			Quantity: draft.Quantity, LimitPrice: draft.LimitPrice,
			ReasonCode: draft.ReasonCode, Reason: draft.Reason,
			CreatedAt: cursor.Timestamp, CausativeCursor: cursor, Ordinal: ordinal,
		}
	}
	if err := w.state.Commit(ctx, w.strategyID, Snapshot{State: result.State, LastCursor: &cursor}, signals); err != nil {
		return nil, fmt.Errorf("commit strategy result: %w", err)
	}
	return signals, nil
}

func CursorForEvent(event domain.MarketEvent) domain.EventCursor {
	return domain.EventCursor{Timestamp: event.ExchangeTime.UTC(), Priority: eventPriority(event.Kind), Sequence: event.Sequence}
}

func eventPriority(kind domain.MarketEventKind) uint16 {
	switch kind {
	case domain.MarketEventTrade:
		return 30
	case domain.MarketEventOrderBook:
		return 40
	case domain.MarketEventCandleClose:
		return 50
	default:
		return 10
	}
}

func CompareCursors(left, right domain.EventCursor) int {
	if left.Timestamp.Before(right.Timestamp) {
		return -1
	}
	if left.Timestamp.After(right.Timestamp) {
		return 1
	}
	if left.Priority < right.Priority {
		return -1
	}
	if left.Priority > right.Priority {
		return 1
	}
	if left.Sequence < right.Sequence {
		return -1
	}
	if left.Sequence > right.Sequence {
		return 1
	}
	return 0
}

type canonicalDraft struct {
	Action     domain.SignalAction `json:"action"`
	OrderType  domain.OrderType    `json:"order_type"`
	Quantity   string              `json:"quantity"`
	LimitValue *string             `json:"limit_value"`
	LimitAsset *string             `json:"limit_asset"`
	ReasonCode string              `json:"reason_code"`
	Reason     string              `json:"reason"`
}

func deterministicSignalID(strategyID domain.StrategyID, cursor domain.EventCursor, ordinal uint16, draft domain.SignalDraft) (domain.SignalID, error) {
	var limitValue, limitAsset *string
	if draft.LimitPrice != nil {
		value, asset := draft.LimitPrice.Value.String(), draft.LimitPrice.Asset
		limitValue, limitAsset = &value, &asset
	}
	payload, err := json.Marshal(canonicalDraft{
		Action: draft.Action, OrderType: draft.OrderType, Quantity: draft.Quantity.Value.String(),
		LimitValue: limitValue, LimitAsset: limitAsset, ReasonCode: draft.ReasonCode, Reason: draft.Reason,
	})
	if err != nil {
		return "", err
	}
	hash := sha256.New()
	writePart := func(value []byte) {
		var size [8]byte
		binary.BigEndian.PutUint64(size[:], uint64(len(value)))
		_, _ = hash.Write(size[:])
		_, _ = hash.Write(value)
	}
	writePart([]byte(SignalIDAlgorithm))
	writePart([]byte(strategyID))
	writePart([]byte(cursor.Timestamp.UTC().Format(time.RFC3339Nano)))
	var number [8]byte
	binary.BigEndian.PutUint16(number[:2], cursor.Priority)
	writePart(number[:2])
	binary.BigEndian.PutUint64(number[:], cursor.Sequence)
	writePart(number[:])
	binary.BigEndian.PutUint16(number[:2], ordinal)
	writePart(number[:2])
	writePart(payload)
	return domain.SignalID(hex.EncodeToString(hash.Sum(nil))), nil
}
