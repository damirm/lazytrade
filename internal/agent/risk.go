package agent

import (
	"context"
	"errors"
	"fmt"
	"sync"

	appclock "github.com/damirm/lazytrade/internal/clock"
	"github.com/damirm/lazytrade/internal/domain"
	"github.com/damirm/lazytrade/internal/risk"
	"github.com/damirm/lazytrade/internal/statistics"
	"github.com/damirm/lazytrade/internal/storage"
	"github.com/shopspring/decimal"
)

type RiskStateStore interface {
	LoadPosition(context.Context, domain.StrategyID, domain.InstrumentID) (storage.Position, error)
	LoadDailyStatistics(context.Context, domain.StrategyID, string, string) (storage.DailyStatistics, error)
}

// PersistentRiskGate rebuilds the risk snapshot from durable position and
// daily statistics before every signal.
type PersistentRiskGate struct {
	mu      sync.Mutex
	manager *risk.Manager
	config  risk.Config
	store   RiskStateStore
	clock   appclock.MutableClock
	status  risk.StrategyStatus
	mark    *domain.Price
}

func NewPersistentRiskGate(config risk.Config, store RiskStateStore, clock appclock.MutableClock) (*PersistentRiskGate, error) {
	if store == nil || clock == nil {
		return nil, errors.New("risk state store and mutable clock are required")
	}
	manager, err := risk.NewManager(config, clock)
	if err != nil {
		return nil, err
	}
	return &PersistentRiskGate{
		manager: manager, config: config, store: store, clock: clock, status: risk.StatusRunning,
	}, nil
}

func (g *PersistentRiskGate) ObserveMarket(_ context.Context, event domain.MarketEvent) error {
	g.mu.Lock()
	defer g.mu.Unlock()
	if err := g.clock.AdvanceTo(event.ExchangeTime); err != nil {
		return err
	}
	switch {
	case event.Candle != nil:
		value := event.Candle.Close
		g.mark = &value
	case event.LastPrice != nil:
		value := *event.LastPrice
		g.mark = &value
	}
	return nil
}

func (g *PersistentRiskGate) Evaluate(ctx context.Context, signal domain.Signal) (RiskDecision, error) {
	g.mu.Lock()
	defer g.mu.Unlock()
	if g.mark == nil {
		return RiskDecision{}, errors.New("risk mark price is unavailable")
	}
	position := decimal.Zero
	storedPosition, err := g.store.LoadPosition(ctx, signal.StrategyID, signal.InstrumentID)
	if err == nil {
		position = storedPosition.Quantity.Value
	} else if !errors.Is(err, storage.ErrNotFound) {
		return RiskDecision{}, fmt.Errorf("load risk position: %w", err)
	}
	components, err := statistics.NewComponents(signal.StrategyID, g.config.SettlementAsset)
	if err != nil {
		return RiskDecision{}, err
	}
	day := g.config.TradingDay.Current(g.clock)
	daily, err := g.store.LoadDailyStatistics(ctx, signal.StrategyID, day.Key, g.config.SettlementAsset)
	if err == nil {
		components.Realized = daily.RealizedPnL
		components.Commissions = daily.Commissions
		components.Funding = daily.Funding
	} else if !errors.Is(err, storage.ErrNotFound) {
		return RiskDecision{}, fmt.Errorf("load daily risk statistics: %w", err)
	}
	if !position.IsZero() {
		average := storedPosition.AveragePrice.Value
		components.Unrealized = g.mark.Value.Sub(average).Mul(position)
	}
	components.UnrealizedComplete = true
	components.FundingComplete = true
	decision := g.manager.Evaluate(signal, risk.Snapshot{
		StrategyID: signal.StrategyID, Status: g.status, TradingDayKey: day.Key,
		SignedPosition: position, MarkPrice: *g.mark, DailyPnL: components,
	})
	if decision.Kind == risk.DecisionPause {
		g.status = risk.StatusRiskPaused
	}
	return RiskDecision{
		Allowed:    decision.Kind == risk.DecisionAllow,
		ReasonCode: string(decision.ReasonCode), Reason: decision.Reason,
	}, nil
}

func (g *PersistentRiskGate) Status() risk.StrategyStatus {
	g.mu.Lock()
	defer g.mu.Unlock()
	return g.status
}
