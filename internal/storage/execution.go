package storage

import (
	"context"
	"encoding/json"
	"time"

	"github.com/damirm/lazytrade/internal/domain"
	"github.com/shopspring/decimal"
)

type OrderIntent struct {
	ID                string
	SignalID          domain.SignalID
	StrategyID        domain.StrategyID
	ExchangeAccountID domain.ExchangeAccountID
	InstrumentID      domain.InstrumentID
	ClientOrderID     domain.ClientOrderID
	Side              domain.OrderSide
	OrderType         domain.OrderType
	Quantity          domain.Quantity
	LimitPrice        *domain.Price
	Status            string
	PayloadChecksum   string
	CreatedAt         time.Time
	UpdatedAt         time.Time
}

type AuditEvent struct {
	ID        string
	EventType string
	Actor     string
	ScopeType string
	ScopeID   string
	Payload   json.RawMessage
	CreatedAt time.Time
}

type IntentLookupStore interface {
	GetOrderIntentByClientOrderID(context.Context, domain.ClientOrderID) (OrderIntent, error)
}

type AuditStore interface {
	AppendAudit(context.Context, AuditEvent) error
	ListAudit(context.Context, uint32) ([]AuditEvent, error)
}

type ExchangeOrder struct {
	ID                string
	OrderIntentID     string
	ExchangeAccountID domain.ExchangeAccountID
	ExchangeOrderID   domain.OrderID
	Status            string
	RequestedQuantity domain.Quantity
	FilledQuantity    domain.Quantity
	AveragePrice      *domain.Price
	SubmittedAt       time.Time
	UpdatedAt         time.Time
}

type IntentResolution struct {
	IntentID string
	Status   string
	Order    *ExchangeOrder
	Audit    AuditEvent
}

// IntentTransition is an atomic compare-and-swap of a durable intent phase
// together with the audit record explaining that transition.
type IntentTransition struct {
	IntentID   string
	FromStatus string
	ToStatus   string
	Audit      AuditEvent
}

type OrderOutboxStore interface {
	ListPendingOrderIntents(context.Context, uint32) ([]OrderIntent, error)
	ListPendingOrderIntentsByStrategy(context.Context, domain.StrategyID, uint32) ([]OrderIntent, error)
	ResolveOrderIntent(context.Context, IntentResolution) error
	TransitionOrderIntent(context.Context, IntentTransition) error
}

type Position struct {
	StrategyID   domain.StrategyID
	InstrumentID domain.InstrumentID
	Quantity     domain.Quantity
	AveragePrice domain.Price
	Revision     uint64
	UpdatedAt    time.Time
}

// OrderCommissionStore applies cumulative exchange commission observations.
// Only a positive delta over the persisted order cumulative value affects P&L.
type OrderCommissionStore interface {
	ApplyCumulativeOrderCommission(context.Context, domain.ExchangeAccountID, domain.OrderID, domain.Money, time.Time, string) (domain.Money, bool, error)
}

type ExecutionInboxEntry struct {
	ID                string
	ExchangeAccountID domain.ExchangeAccountID
	SourceFamily      string
	DedupeKey         string
	PayloadChecksum   string
	Execution         domain.Execution
	TradingDay        string
	Status            string
	ReceivedAt        time.Time
	AppliedAt         *time.Time
}

// ExecutionInboxStore is the durable ingress boundary for exchange fills.
// Applying an entry must update every projection and mark the entry applied
// in one transaction.
type ExecutionInboxStore interface {
	StageExecution(context.Context, domain.ExchangeAccountID, domain.Execution, time.Time, string) (ExecutionInboxEntry, bool, error)
	ListPendingExecutions(context.Context, domain.ExchangeAccountID, uint32) ([]ExecutionInboxEntry, error)
	ApplyStagedExecution(context.Context, string) (bool, error)
}

type ExecutionHistoryCheckpoint struct {
	ExchangeAccountID domain.ExchangeAccountID
	Source            string
	CoveredThrough    time.Time
	CreatedAt         time.Time
}

// ExecutionHistoryCheckpointStore records the end of the latest completely
// scanned history interval. Callers may deliberately start an overlapping
// scan before CoveredThrough; execution inbox deduplication makes that safe.
type ExecutionHistoryCheckpointStore interface {
	LoadExecutionHistoryCheckpoint(context.Context, domain.ExchangeAccountID, string) (ExecutionHistoryCheckpoint, error)
	AdvanceExecutionHistoryCheckpoint(context.Context, ExecutionHistoryCheckpoint) error
}

type DailyStatistics struct {
	StrategyID    domain.StrategyID
	TradingDay    string
	Asset         string
	RealizedPnL   decimal.Decimal
	UnrealizedPnL decimal.Decimal
	TotalPnL      decimal.Decimal
	Commissions   decimal.Decimal
	Funding       decimal.Decimal
	TradeCount    uint64
	Complete      bool
	UpdatedAt     time.Time
}

type LocalOpenOrder struct {
	StrategyID        domain.StrategyID
	InstrumentID      domain.InstrumentID
	ClientOrderID     domain.ClientOrderID
	ExchangeOrderID   domain.OrderID
	Side              domain.OrderSide
	Status            string
	RequestedQuantity domain.Quantity
	FilledQuantity    domain.Quantity
}

type ReconciliationStore interface {
	ListPositionsByExchange(context.Context, domain.ExchangeAccountID) ([]Position, error)
	ListOpenOrdersByExchange(context.Context, domain.ExchangeAccountID) ([]LocalOpenOrder, error)
}
