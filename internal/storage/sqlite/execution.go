package sqlite

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"

	"github.com/damirm/lazytrade/internal/domain"
	"github.com/damirm/lazytrade/internal/storage"
	"github.com/damirm/lazytrade/internal/storage/sqlite/generated"
)

func (s *Store) GetOrderIntentByClientOrderID(ctx context.Context, id domain.ClientOrderID) (storage.OrderIntent, error) {
	row, err := s.queries.GetOrderIntentByClientOrderID(ctx, string(id))
	if errors.Is(err, sql.ErrNoRows) {
		return storage.OrderIntent{}, fmt.Errorf("order intent %s: %w", id, storage.ErrNotFound)
	}
	if err != nil {
		return storage.OrderIntent{}, fmt.Errorf("sqlite: get order intent %s: %w", id, err)
	}
	return decodeOrderIntent(row)
}

func decodeOrderIntent(row generated.OrderIntent) (storage.OrderIntent, error) {
	quantity, err := domain.NewQuantity(row.Quantity)
	if err != nil {
		return storage.OrderIntent{}, fmt.Errorf("sqlite: decode intent quantity: %w", err)
	}
	var price *domain.Price
	if row.LimitPrice.Valid != row.PriceAsset.Valid {
		return storage.OrderIntent{}, errors.New("sqlite: incomplete intent limit price")
	}
	if row.LimitPrice.Valid {
		value, err := domain.NewPrice(row.LimitPrice.String, row.PriceAsset.String)
		if err != nil {
			return storage.OrderIntent{}, fmt.Errorf("sqlite: decode intent price: %w", err)
		}
		price = &value
	}
	return storage.OrderIntent{
		ID: row.ID, SignalID: domain.SignalID(row.SignalID), StrategyID: domain.StrategyID(row.StrategyID),
		ExchangeAccountID: domain.ExchangeAccountID(row.ExchangeAccountID),
		InstrumentID:      domain.InstrumentID(row.InstrumentID), ClientOrderID: domain.ClientOrderID(row.ClientOrderID),
		Side: domain.OrderSide(row.Side), OrderType: domain.OrderType(row.OrderType), Quantity: quantity,
		LimitPrice: price, Status: row.Status, PayloadChecksum: row.PayloadChecksum,
		CreatedAt: fromMicros(row.CreatedAt), UpdatedAt: fromMicros(row.UpdatedAt),
	}, nil
}

func (s *Store) ListPendingOrderIntents(ctx context.Context, limit uint32) ([]storage.OrderIntent, error) {
	if limit == 0 {
		limit = 100
	}
	if limit > 1000 {
		return nil, errors.New("sqlite: pending intent limit exceeds 1000")
	}
	rows, err := s.queries.ListPendingOrderIntents(ctx, int64(limit))
	if err != nil {
		return nil, fmt.Errorf("sqlite: list pending order intents: %w", err)
	}
	result := make([]storage.OrderIntent, 0, len(rows))
	for _, row := range rows {
		intent, err := decodeOrderIntent(row)
		if err != nil {
			return nil, err
		}
		result = append(result, intent)
	}
	return result, nil
}

func (s *Store) ListPendingOrderIntentsByStrategy(
	ctx context.Context,
	strategyID domain.StrategyID,
	limit uint32,
) ([]storage.OrderIntent, error) {
	if err := strategyID.Validate(); err != nil {
		return nil, fmt.Errorf("sqlite: pending intent strategy: %w", err)
	}
	if limit == 0 {
		limit = 100
	}
	if limit > 1000 {
		return nil, errors.New("sqlite: pending intent limit exceeds 1000")
	}
	rows, err := s.queries.ListPendingOrderIntentsByStrategy(
		ctx,
		generated.ListPendingOrderIntentsByStrategyParams{
			StrategyID: string(strategyID),
			Limit:      int64(limit),
		},
	)
	if err != nil {
		return nil, fmt.Errorf("sqlite: list strategy pending order intents: %w", err)
	}
	result := make([]storage.OrderIntent, 0, len(rows))
	for _, row := range rows {
		intent, err := decodeOrderIntent(row)
		if err != nil {
			return nil, err
		}
		result = append(result, intent)
	}
	return result, nil
}

func (s *Store) ResolveOrderIntent(ctx context.Context, resolution storage.IntentResolution) error {
	if resolution.IntentID == "" ||
		(resolution.Status != "submitted" && resolution.Status != "rejected" &&
			resolution.Status != "unknown" && resolution.Status != "failed" &&
			resolution.Status != "not_submitted") {
		return errors.New("sqlite: invalid intent resolution")
	}
	if (resolution.Status == "submitted") != (resolution.Order != nil) {
		return errors.New("sqlite: submitted resolution must contain exactly one exchange order")
	}
	updatedAt, err := micros(resolution.Audit.CreatedAt)
	if err != nil {
		return err
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return fmt.Errorf("sqlite: begin intent resolution: %w", err)
	}
	defer tx.Rollback()
	q := generated.New(tx)
	affected, err := q.ResolveOrderIntent(ctx, generated.ResolveOrderIntentParams{
		Status: resolution.Status, UpdatedAt: updatedAt, ID: resolution.IntentID,
	})
	if err != nil {
		return fmt.Errorf("sqlite: resolve order intent: %w", err)
	}
	if affected != 1 {
		return fmt.Errorf("order intent %s is not unresolved: %w", resolution.IntentID, storage.ErrConflict)
	}
	if resolution.Order != nil {
		params, err := exchangeOrderParams(*resolution.Order)
		if err != nil {
			return err
		}
		if resolution.Order.OrderIntentID != resolution.IntentID {
			return errors.New("sqlite: exchange order belongs to another intent")
		}
		if err := q.InsertExchangeOrder(ctx, params); err != nil {
			return fmt.Errorf("sqlite: insert exchange order: %w", storage.ErrConflict)
		}
	}
	if err := appendAudit(ctx, q, resolution.Audit); err != nil {
		return err
	}
	if err := tx.Commit(); err != nil {
		return fmt.Errorf("sqlite: commit intent resolution: %w", err)
	}
	return nil
}

func (s *Store) TransitionOrderIntent(ctx context.Context, transition storage.IntentTransition) error {
	if transition.IntentID == "" || !validIntentPhase(transition.FromStatus) ||
		!validIntentPhase(transition.ToStatus) || transition.FromStatus == transition.ToStatus {
		return errors.New("sqlite: invalid intent transition")
	}
	updatedAt, err := micros(transition.Audit.CreatedAt)
	if err != nil {
		return err
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return fmt.Errorf("sqlite: begin intent transition: %w", err)
	}
	defer tx.Rollback()
	q := generated.New(tx)
	affected, err := q.TransitionOrderIntent(ctx, generated.TransitionOrderIntentParams{
		Status: transition.ToStatus, UpdatedAt: updatedAt, ID: transition.IntentID,
		Status_2: transition.FromStatus,
	})
	if err != nil {
		return fmt.Errorf("sqlite: transition order intent: %w", err)
	}
	if affected != 1 {
		return fmt.Errorf("order intent %s is not %s: %w", transition.IntentID, transition.FromStatus, storage.ErrConflict)
	}
	if err := appendAudit(ctx, q, transition.Audit); err != nil {
		return err
	}
	if err := tx.Commit(); err != nil {
		return fmt.Errorf("sqlite: commit intent transition: %w", err)
	}
	return nil
}

func validIntentPhase(status string) bool {
	switch status {
	case "ready", "submitting", "submitted", "rejected", "unknown", "failed", "not_submitted":
		return true
	default:
		return false
	}
}

func (s *Store) ListSignalsPendingRisk(ctx context.Context, limit uint32) ([]domain.Signal, error) {
	if limit == 0 {
		limit = 100
	}
	if limit > 1000 {
		return nil, errors.New("sqlite: pending signal limit exceeds 1000")
	}
	rows, err := s.queries.ListSignalsPendingRisk(ctx, int64(limit))
	if err != nil {
		return nil, fmt.Errorf("sqlite: list signals pending risk: %w", err)
	}
	result := make([]domain.Signal, 0, len(rows))
	for _, row := range rows {
		quantity, err := domain.NewQuantity(row.Quantity)
		if err != nil {
			return nil, fmt.Errorf("sqlite: decode signal quantity: %w", err)
		}
		var limitPrice *domain.Price
		if row.LimitPrice.Valid != row.PriceAsset.Valid {
			return nil, errors.New("sqlite: incomplete signal limit price")
		}
		if row.LimitPrice.Valid {
			price, err := domain.NewPrice(row.LimitPrice.String, row.PriceAsset.String)
			if err != nil {
				return nil, fmt.Errorf("sqlite: decode signal limit price: %w", err)
			}
			limitPrice = &price
		}
		if row.Ordinal < 0 || row.Ordinal > int64(^uint16(0)) ||
			row.CursorPriority < 0 || row.CursorPriority > int64(^uint16(0)) ||
			row.CursorSequence < 0 {
			return nil, errors.New("sqlite: invalid persisted signal cursor")
		}
		signal := domain.Signal{
			ID: domain.SignalID(row.ID), StrategyID: domain.StrategyID(row.StrategyID),
			ExchangeAccountID: domain.ExchangeAccountID(row.ExchangeAccountID),
			InstrumentID:      domain.InstrumentID(row.InstrumentID),
			Action:            domain.SignalAction(row.Action), OrderType: domain.OrderType(row.OrderType),
			Quantity: quantity, LimitPrice: limitPrice, ReasonCode: row.ReasonCode, Reason: row.Reason,
			CreatedAt: fromMicros(row.CreatedAt),
			CausativeCursor: domain.EventCursor{
				Timestamp: fromMicros(row.CursorTimestamp), Priority: uint16(row.CursorPriority),
				Sequence: uint64(row.CursorSequence),
			},
			Ordinal: uint16(row.Ordinal),
		}
		if err := signal.Validate(); err != nil {
			return nil, fmt.Errorf("sqlite: decode pending signal: %w", err)
		}
		result = append(result, signal)
	}
	return result, nil
}

func (s *Store) ListSignalsPendingRiskByStrategy(
	ctx context.Context,
	strategyID domain.StrategyID,
	limit uint32,
) ([]domain.Signal, error) {
	if err := strategyID.Validate(); err != nil {
		return nil, fmt.Errorf("sqlite: pending signal strategy: %w", err)
	}
	if limit == 0 {
		limit = 100
	}
	if limit > 1000 {
		return nil, errors.New("sqlite: pending signal limit exceeds 1000")
	}
	rows, err := s.queries.ListSignalsPendingRiskByStrategy(
		ctx,
		generated.ListSignalsPendingRiskByStrategyParams{
			StrategyID: string(strategyID),
			Limit:      int64(limit),
		},
	)
	if err != nil {
		return nil, fmt.Errorf("sqlite: list strategy signals pending risk: %w", err)
	}
	result := make([]domain.Signal, 0, len(rows))
	for _, row := range rows {
		quantity, err := domain.NewQuantity(row.Quantity)
		if err != nil {
			return nil, fmt.Errorf("sqlite: decode signal quantity: %w", err)
		}
		var limitPrice *domain.Price
		if row.LimitPrice.Valid != row.PriceAsset.Valid {
			return nil, errors.New("sqlite: incomplete signal limit price")
		}
		if row.LimitPrice.Valid {
			price, err := domain.NewPrice(row.LimitPrice.String, row.PriceAsset.String)
			if err != nil {
				return nil, fmt.Errorf("sqlite: decode signal limit price: %w", err)
			}
			limitPrice = &price
		}
		if row.Ordinal < 0 || row.Ordinal > int64(^uint16(0)) ||
			row.CursorPriority < 0 || row.CursorPriority > int64(^uint16(0)) ||
			row.CursorSequence < 0 {
			return nil, errors.New("sqlite: invalid persisted signal cursor")
		}
		signal := domain.Signal{
			ID: domain.SignalID(row.ID), StrategyID: domain.StrategyID(row.StrategyID),
			ExchangeAccountID: domain.ExchangeAccountID(row.ExchangeAccountID),
			InstrumentID:      domain.InstrumentID(row.InstrumentID),
			Action:            domain.SignalAction(row.Action), OrderType: domain.OrderType(row.OrderType),
			Quantity: quantity, LimitPrice: limitPrice, ReasonCode: row.ReasonCode, Reason: row.Reason,
			CreatedAt: fromMicros(row.CreatedAt),
			CausativeCursor: domain.EventCursor{
				Timestamp: fromMicros(row.CursorTimestamp), Priority: uint16(row.CursorPriority),
				Sequence: uint64(row.CursorSequence),
			},
			Ordinal: uint16(row.Ordinal),
		}
		if err := signal.Validate(); err != nil {
			return nil, fmt.Errorf("sqlite: decode pending signal: %w", err)
		}
		result = append(result, signal)
	}
	return result, nil
}

func (s *Store) RecordRiskDecision(ctx context.Context, decision storage.RiskDecision, audit storage.AuditEvent) error {
	if decision.Decision == "allow" {
		return errors.New("sqlite: allowed risk decision requires an order intent")
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return fmt.Errorf("sqlite: begin risk decision: %w", err)
	}
	defer tx.Rollback()
	q := generated.New(tx)
	if err := insertRiskDecision(ctx, q, decision); err != nil {
		return err
	}
	if err := appendAudit(ctx, q, audit); err != nil {
		return err
	}
	if err := tx.Commit(); err != nil {
		return fmt.Errorf("sqlite: commit risk decision: %w", err)
	}
	return nil
}

func (s *Store) RecordAllowedDecisionIntent(ctx context.Context, decision storage.RiskDecision, intent storage.OrderIntent, audit storage.AuditEvent) error {
	if decision.Decision != "allow" || decision.SignalID != intent.SignalID {
		return errors.New("sqlite: allowed decision and intent do not match")
	}
	params, err := intentParams(intent)
	if err != nil {
		return err
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return fmt.Errorf("sqlite: begin allowed intent: %w", err)
	}
	defer tx.Rollback()
	q := generated.New(tx)
	if err := insertRiskDecision(ctx, q, decision); err != nil {
		return err
	}
	if err := q.InsertOrderIntent(ctx, params); err != nil {
		return fmt.Errorf("sqlite: insert allowed order intent: %w", storage.ErrConflict)
	}
	if err := appendAudit(ctx, q, audit); err != nil {
		return err
	}
	if err := tx.Commit(); err != nil {
		return fmt.Errorf("sqlite: commit allowed intent: %w", err)
	}
	return nil
}

func insertRiskDecision(ctx context.Context, q *generated.Queries, decision storage.RiskDecision) error {
	if decision.ID == "" || decision.SignalID.Validate() != nil ||
		(decision.Decision != "allow" && decision.Decision != "reject" && decision.Decision != "pause") ||
		decision.ReasonCode == "" || !json.Valid(decision.Payload) {
		return errors.New("sqlite: invalid risk decision")
	}
	createdAt, err := micros(decision.CreatedAt)
	if err != nil {
		return err
	}
	if err := q.InsertRiskDecision(ctx, generated.InsertRiskDecisionParams{
		ID: decision.ID, SignalID: string(decision.SignalID), Decision: decision.Decision,
		ReasonCode: decision.ReasonCode, Payload: string(decision.Payload), CreatedAt: createdAt,
	}); err != nil {
		return fmt.Errorf("sqlite: insert risk decision %s: %w", decision.ID, storage.ErrConflict)
	}
	return nil
}

func (s *Store) AppendAudit(ctx context.Context, event storage.AuditEvent) error {
	return appendAudit(ctx, s.queries, event)
}

func appendAudit(ctx context.Context, q *generated.Queries, event storage.AuditEvent) error {
	if event.ID == "" || event.EventType == "" || event.Actor == "" || event.ScopeType == "" ||
		event.ScopeID == "" || !json.Valid(event.Payload) {
		return errors.New("sqlite: invalid audit event")
	}
	created, err := micros(event.CreatedAt)
	if err != nil {
		return err
	}
	if err := q.InsertAuditEvent(ctx, generated.InsertAuditEventParams{
		ID: event.ID, EventType: event.EventType, Actor: event.Actor,
		ScopeType: event.ScopeType, ScopeID: event.ScopeID,
		Payload: string(event.Payload), CreatedAt: created,
	}); err != nil {
		return fmt.Errorf("audit event %s: %w", event.ID, storage.ErrConflict)
	}
	return nil
}

func (s *Store) ListAudit(ctx context.Context, limit uint32) ([]storage.AuditEvent, error) {
	if limit == 0 {
		limit = 100
	}
	if limit > 1000 {
		return nil, errors.New("sqlite: audit limit exceeds 1000")
	}
	rows, err := s.queries.ListAuditEvents(ctx, int64(limit))
	if err != nil {
		return nil, fmt.Errorf("sqlite: list audit events: %w", err)
	}
	result := make([]storage.AuditEvent, 0, len(rows))
	for _, row := range rows {
		result = append(result, storage.AuditEvent{
			ID: row.ID, EventType: row.EventType, Actor: row.Actor,
			ScopeType: row.ScopeType, ScopeID: row.ScopeID,
			Payload: json.RawMessage(row.Payload), CreatedAt: fromMicros(row.CreatedAt),
		})
	}
	return result, nil
}
