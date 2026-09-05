package backtest

import (
	"context"
	"fmt"

	appclock "github.com/damirm/lazytrade/internal/clock"
	"github.com/damirm/lazytrade/internal/domain"
	"github.com/damirm/lazytrade/internal/risk"
	"github.com/damirm/lazytrade/internal/statistics"
)

// ManagedRiskEvaluator connects the shared live risk manager and P&L reducer
// to the deterministic backtest event loop.
type ManagedRiskEvaluator struct {
	manager  *risk.Manager
	config   risk.Config
	clock    appclock.Clock
	state    statistics.State
	baseline statistics.Components
	dayKey   string
	status   risk.StrategyStatus
}

func NewManagedRiskEvaluator(config risk.Config, clock appclock.Clock) (*ManagedRiskEvaluator, error) {
	manager, err := risk.NewManager(config, clock)
	if err != nil {
		return nil, err
	}
	state, err := statistics.NewState(config.StrategyID, config.SettlementAsset)
	if err != nil {
		return nil, err
	}
	return &ManagedRiskEvaluator{
		manager: manager, config: config, clock: clock, state: state,
		baseline: state.Components, status: risk.StatusRunning,
	}, nil
}

func (m *ManagedRiskEvaluator) Observe(_ context.Context, event domain.MarketEvent, executions []domain.Execution, portfolio PortfolioSnapshot) (RiskDecision, error) {
	for _, execution := range executions {
		next, err := statistics.ApplyExecution(m.state, execution)
		if err != nil {
			return RiskDecision{}, fmt.Errorf("apply execution: %w", err)
		}
		m.state = next
	}
	if portfolio.LastPrice != nil {
		next, err := statistics.MarkToMarket(m.state, *portfolio.LastPrice, true)
		if err != nil {
			return RiskDecision{}, fmt.Errorf("mark to market: %w", err)
		}
		m.state = next
	}
	day := m.config.TradingDay.At(event.ExchangeTime)
	if m.dayKey == "" || day.Key != m.dayKey {
		m.dayKey = day.Key
		m.baseline = m.state.Components
	}
	daily, err := m.state.Components.Delta(m.baseline)
	if err != nil {
		return RiskDecision{}, err
	}
	decision := m.manager.ObservePnL(m.snapshot(daily, portfolio))
	return m.convert(decision), nil
}

func (m *ManagedRiskEvaluator) Evaluate(_ context.Context, signal domain.Signal, portfolio PortfolioSnapshot) (RiskDecision, error) {
	daily, err := m.state.Components.Delta(m.baseline)
	if err != nil {
		return RiskDecision{}, err
	}
	return m.convert(m.manager.Evaluate(signal, m.snapshot(daily, portfolio))), nil
}

func (m *ManagedRiskEvaluator) snapshot(daily statistics.Components, portfolio PortfolioSnapshot) risk.Snapshot {
	mark := domain.Price{Asset: m.config.SettlementAsset}
	if portfolio.LastPrice != nil {
		mark = *portfolio.LastPrice
	}
	return risk.Snapshot{
		StrategyID: m.config.StrategyID, Status: m.status, TradingDayKey: m.dayKey,
		SignedPosition: m.state.Position.Quantity, MarkPrice: mark, DailyPnL: daily,
	}
}

func (m *ManagedRiskEvaluator) convert(decision risk.Decision) RiskDecision {
	if decision.Kind == risk.DecisionPause {
		m.status = risk.StatusRiskPaused
	}
	return RiskDecision{
		Allowed:    decision.Kind == risk.DecisionAllow,
		Paused:     decision.Kind == risk.DecisionPause,
		ReasonCode: string(decision.ReasonCode),
	}
}

func (m *ManagedRiskEvaluator) Status() risk.StrategyStatus { return m.status }

// Resume requires the same explicit confirmation as live risk management.
func (m *ManagedRiskEvaluator) Resume(explicitlyConfirmed bool) error {
	status, err := risk.Resume(m.status, explicitlyConfirmed)
	if err == nil {
		m.status = status
	}
	return err
}

func (m *ManagedRiskEvaluator) Components() statistics.Components { return m.state.Components }
