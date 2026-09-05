package risk

import (
	"errors"
	"fmt"
	"time"

	appclock "github.com/damirm/lazytrade/internal/clock"
	"github.com/damirm/lazytrade/internal/domain"
	"github.com/damirm/lazytrade/internal/statistics"
	"github.com/shopspring/decimal"
)

type DecisionKind uint8

const (
	DecisionAllow DecisionKind = iota + 1
	DecisionReject
	DecisionPause
)

type ReasonCode string

const (
	ReasonAssetMismatch      ReasonCode = "asset_mismatch"
	ReasonMaxPositionValue   ReasonCode = "max_position_value"
	ReasonMaxDailyLoss       ReasonCode = "max_daily_loss"
	ReasonStrategyNotRunning ReasonCode = "strategy_not_running"
	ReasonInvalidSignal      ReasonCode = "invalid_signal"
	ReasonIncompletePnL      ReasonCode = "incomplete_pnl"
)

type PnLMode string

const (
	PnLRealized PnLMode = "realized"
	PnLTotal    PnLMode = "total"
)

type DailyLossLimit struct {
	Limit domain.Money
	Mode  PnLMode
}

type Config struct {
	StrategyID       domain.StrategyID
	SettlementAsset  string
	MaxPositionValue *domain.Money
	MaxDailyLoss     *DailyLossLimit
	TradingDay       TradingDayPolicy
}

type Snapshot struct {
	StrategyID     domain.StrategyID
	Status         StrategyStatus
	TradingDayKey  string
	SignedPosition decimal.Decimal
	MarkPrice      domain.Price
	DailyPnL       statistics.Components
}

type Decision struct {
	Kind            DecisionKind
	SignalID        domain.SignalID
	ReasonCode      ReasonCode
	Reason          string
	EvaluatedAt     time.Time
	EffectiveStatus StrategyStatus
}

type Manager struct {
	config Config
	clock  appclock.Clock
}

func NewManager(config Config, clock appclock.Clock) (*Manager, error) {
	if clock == nil {
		return nil, errors.New("clock is required")
	}
	if err := config.StrategyID.Validate(); err != nil {
		return nil, fmt.Errorf("strategy ID: %w", err)
	}
	asset, err := domain.NormalizeAsset(config.SettlementAsset)
	if err != nil {
		return nil, fmt.Errorf("settlement asset: %w", err)
	}
	if asset != config.SettlementAsset {
		return nil, errors.New("settlement asset must be normalized")
	}
	if money := config.MaxPositionValue; money != nil {
		if err := money.Validate(); err != nil {
			return nil, fmt.Errorf("max position value: %w", err)
		}
		if !money.Amount.IsPositive() {
			return nil, errors.New("max position value must be positive")
		}
		if money.Asset != asset {
			return nil, fmt.Errorf("max position value: %w", domain.ErrAssetMismatch)
		}
	}
	if limit := config.MaxDailyLoss; limit != nil {
		if err := limit.Limit.Validate(); err != nil {
			return nil, fmt.Errorf("max daily loss: %w", err)
		}
		if !limit.Limit.Amount.IsPositive() {
			return nil, errors.New("max daily loss must be positive")
		}
		if limit.Limit.Asset != asset {
			return nil, fmt.Errorf("max daily loss: %w", domain.ErrAssetMismatch)
		}
		if limit.Mode != PnLRealized && limit.Mode != PnLTotal {
			return nil, fmt.Errorf("unsupported P&L mode %q", limit.Mode)
		}
	}
	if err := config.TradingDay.Validate(); err != nil {
		return nil, err
	}
	return &Manager{config: config, clock: clock}, nil
}

func (m *Manager) Evaluate(signal domain.Signal, snapshot Snapshot) Decision {
	now := m.clock.Now()
	if signal.Validate() != nil || signal.StrategyID != m.config.StrategyID {
		return reject(signal.ID, now, snapshot.Status, ReasonInvalidSignal, "signal is invalid or belongs to another strategy")
	}
	if reason := m.validateSnapshot(snapshot); reason != "" {
		return pause(signal.ID, now, ReasonAssetMismatch, reason)
	}
	if snapshot.Status != StatusRunning {
		return reject(signal.ID, now, snapshot.Status, ReasonStrategyNotRunning, "strategy is not running")
	}
	if decision := m.observe(signal.ID, snapshot, now); decision.Kind == DecisionPause {
		return decision
	}
	if m.config.MaxPositionValue != nil {
		price := snapshot.MarkPrice
		if signal.LimitPrice != nil {
			price = *signal.LimitPrice
		}
		if price.Asset != m.config.SettlementAsset {
			return pause(signal.ID, now, ReasonAssetMismatch, "signal valuation asset differs from settlement asset")
		}
		projected := projectedPosition(snapshot.SignedPosition, signal)
		value := projected.Abs().Mul(price.Value)
		if value.GreaterThan(m.config.MaxPositionValue.Amount) {
			return reject(signal.ID, now, StatusRunning, ReasonMaxPositionValue,
				fmt.Sprintf("projected position value %s %s exceeds limit %s %s",
					value, price.Asset, m.config.MaxPositionValue.Amount, m.config.MaxPositionValue.Asset))
		}
	}
	return Decision{Kind: DecisionAllow, SignalID: signal.ID, EvaluatedAt: now, EffectiveStatus: StatusRunning}
}

// ObservePnL enforces the daily limit even when no signal is being evaluated.
func (m *Manager) ObservePnL(snapshot Snapshot) Decision {
	now := m.clock.Now()
	if reason := m.validateSnapshot(snapshot); reason != "" {
		return pause("", now, ReasonAssetMismatch, reason)
	}
	if snapshot.Status != StatusRunning {
		return reject("", now, snapshot.Status, ReasonStrategyNotRunning, "strategy is not running")
	}
	return m.observe("", snapshot, now)
}

func (m *Manager) observe(signalID domain.SignalID, snapshot Snapshot, now time.Time) Decision {
	limit := m.config.MaxDailyLoss
	if limit == nil {
		return Decision{Kind: DecisionAllow, SignalID: signalID, EvaluatedAt: now, EffectiveStatus: snapshot.Status}
	}
	var amount decimal.Decimal
	switch limit.Mode {
	case PnLRealized:
		amount = snapshot.DailyPnL.RealizedNet().Amount
	case PnLTotal:
		total, err := snapshot.DailyPnL.Total()
		if err != nil {
			return pause(signalID, now, ReasonIncompletePnL, "total daily P&L is incomplete")
		}
		amount = total.Amount
	}
	// Equality is a breach: P&L <= -limit.
	if amount.LessThanOrEqual(limit.Limit.Amount.Neg()) {
		return pause(signalID, now, ReasonMaxDailyLoss,
			fmt.Sprintf("daily P&L %s %s reached loss limit %s %s",
				amount, limit.Limit.Asset, limit.Limit.Amount, limit.Limit.Asset))
	}
	return Decision{Kind: DecisionAllow, SignalID: signalID, EvaluatedAt: now, EffectiveStatus: snapshot.Status}
}

func (m *Manager) validateSnapshot(snapshot Snapshot) string {
	if snapshot.StrategyID != m.config.StrategyID || snapshot.DailyPnL.StrategyID != m.config.StrategyID {
		return "snapshot belongs to another strategy"
	}
	if snapshot.MarkPrice.Asset != m.config.SettlementAsset ||
		snapshot.DailyPnL.Asset != m.config.SettlementAsset {
		return "snapshot asset differs from settlement asset"
	}
	if snapshot.TradingDayKey != m.config.TradingDay.Current(m.clock).Key {
		return "snapshot does not belong to the current trading day"
	}
	return ""
}

func projectedPosition(current decimal.Decimal, signal domain.Signal) decimal.Decimal {
	switch signal.Action {
	case domain.SignalBuy:
		return current.Add(signal.Quantity.Value)
	case domain.SignalSell:
		return current.Sub(signal.Quantity.Value)
	case domain.SignalClose:
		return decimal.Zero
	default:
		return current
	}
}

func reject(signalID domain.SignalID, at time.Time, status StrategyStatus, code ReasonCode, reason string) Decision {
	return Decision{Kind: DecisionReject, SignalID: signalID, ReasonCode: code, Reason: reason, EvaluatedAt: at, EffectiveStatus: status}
}

func pause(signalID domain.SignalID, at time.Time, code ReasonCode, reason string) Decision {
	return Decision{Kind: DecisionPause, SignalID: signalID, ReasonCode: code, Reason: reason, EvaluatedAt: at, EffectiveStatus: StatusRiskPaused}
}
