package storage

import (
	"context"
	"encoding/json"
	"time"

	"github.com/damirm/lazytrade/internal/domain"
)

type RiskDecision struct {
	ID         string
	SignalID   domain.SignalID
	Decision   string
	ReasonCode string
	Payload    json.RawMessage
	CreatedAt  time.Time
}

// SignalOutboxStore closes the crash gap between the atomic strategy
// state/signal commit and order submission.
type SignalOutboxStore interface {
	ListSignalsPendingRisk(context.Context, uint32) ([]domain.Signal, error)
	ListSignalsPendingRiskByStrategy(context.Context, domain.StrategyID, uint32) ([]domain.Signal, error)
	RecordRiskDecision(context.Context, RiskDecision, AuditEvent) error
	RecordAllowedDecisionIntent(context.Context, RiskDecision, OrderIntent, AuditEvent) error
}
