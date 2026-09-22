package agent

import (
	"context"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"time"

	"github.com/damirm/lazytrade/internal/domain"
	"github.com/damirm/lazytrade/internal/exchange"
	"github.com/damirm/lazytrade/internal/storage"
	"github.com/damirm/lazytrade/internal/strategy"
)

type RiskDecision struct {
	Allowed    bool
	ReasonCode string
	Reason     string
}

type SignalRisk interface {
	Evaluate(context.Context, domain.Signal) (RiskDecision, error)
}

type MarketRiskObserver interface {
	ObserveMarket(context.Context, domain.MarketEvent) error
}

type StrategyBinding struct {
	ID            domain.StrategyID
	InstrumentID  domain.InstrumentID
	Worker        *strategy.Worker
	Risk          SignalRisk
	Subscription  exchange.Subscription
	TradingDayKey func(time.Time) string
}

type Store interface {
	storage.IntentLookupStore
	storage.ExecutionOwnerStore
	storage.SignalOutboxStore
	storage.OrderOutboxStore
	storage.ExecutionInboxStore
	storage.OrderCommissionStore
	storage.ExecutionHistoryCheckpointStore
}

var discardLogger = slog.New(slog.NewTextHandler(io.Discard, nil))

type Runtime struct {
	Logger                 *slog.Logger
	Exchange               exchange.Exchange
	Strategies             []StrategyBinding
	Intents                Store
	Lifecycle              LifecycleStore
	Ready                  chan<- struct{}
	OnOrder                func(domain.Order)
	OnStrategyError        func(domain.StrategyID, error)
	Now                    func() time.Time
	Reconciler             StartupReconciler
	HistorySource          string
	HistoryBootstrap       time.Duration
	HistoryOverlap         time.Duration
	HistoryVisibilityDelay time.Duration
}

type RuntimeConfig struct {
	Logger                 *slog.Logger
	Exchange               exchange.Exchange
	Strategies             []StrategyBinding
	Store                  Store
	Lifecycle              LifecycleStore
	Ready                  chan<- struct{}
	OnOrder                func(domain.Order)
	OnStrategyError        func(domain.StrategyID, error)
	Now                    func() time.Time
	Reconciler             StartupReconciler
	HistorySource          string
	HistoryBootstrap       time.Duration
	HistoryOverlap         time.Duration
	HistoryVisibilityDelay time.Duration
}

func NewRuntime(config RuntimeConfig) (*Runtime, error) {
	runtime := &Runtime{
		Logger: config.Logger, Exchange: config.Exchange, Strategies: config.Strategies,
		Intents: config.Store, Lifecycle: config.Lifecycle, Ready: config.Ready,
		OnOrder: config.OnOrder, OnStrategyError: config.OnStrategyError, Now: config.Now,
		Reconciler: config.Reconciler, HistorySource: config.HistorySource,
		HistoryBootstrap: config.HistoryBootstrap, HistoryOverlap: config.HistoryOverlap,
		HistoryVisibilityDelay: config.HistoryVisibilityDelay,
	}
	if runtime.Exchange == nil || runtime.Intents == nil {
		return nil, errors.New("exchange and intent store are required")
	}
	if _, _, _, err := runtime.components(); err != nil {
		return nil, err
	}
	return runtime, nil
}

func (r Runtime) Run(ctx context.Context) (resultErr error) {
	logger := r.logger()
	if r.Exchange == nil || r.Intents == nil {
		return errors.New("exchange and intent store are required")
	}
	workers, risks, subscriptions, err := r.components()
	if err != nil {
		return err
	}
	logger.InfoContext(ctx, "agent starting", "event", "agent_starting", "exchange", r.Exchange.Name(), "strategies", len(workers), "subscriptions", len(subscriptions))
	defer func() {
		if resultErr == nil || errors.Is(resultErr, context.Canceled) {
			logger.Info("agent stopped", "event", "agent_stopped")
			return
		}
		logger.Error("agent stopped with error", "event", "agent_failed", "error", resultErr)
	}()
	lifecycleIDs := r.lifecycleStrategyIDs()
	if r.Lifecycle != nil && len(lifecycleIDs) == 0 {
		return errors.New("strategy IDs are required for lifecycle persistence")
	}
	if err := r.repairFailedStrategySignals(ctx, lifecycleIDs); err != nil {
		return fmt.Errorf("repair failed strategy signals: %w", err)
	}
	if err := r.setLifecycle(ctx, lifecycleIDs, RuntimeStatusReconciling, "startup"); err != nil {
		return fmt.Errorf("persist reconciling lifecycle: %w", err)
	}
	failedStrategies := make(map[domain.StrategyID]struct{})
	if r.Lifecycle != nil {
		defer func() {
			terminalIDs := make([]domain.StrategyID, 0, len(lifecycleIDs))
			for _, strategyID := range lifecycleIDs {
				if _, failed := failedStrategies[strategyID]; !failed {
					terminalIDs = append(terminalIDs, strategyID)
				}
			}
			statusCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), 5*time.Second)
			defer cancel()
			if err := r.setTerminalLifecycle(statusCtx, terminalIDs, resultErr); err != nil {
				resultErr = errors.Join(resultErr, fmt.Errorf("persist terminal lifecycle: %w", err))
			}
		}()
	}
	state, err := r.startup(ctx, workers, risks, subscriptions, lifecycleIDs)
	if err != nil {
		return err
	}
	return r.runMarketLoop(ctx, state, failedStrategies)
}

func (r Runtime) components() (
	map[domain.InstrumentID]*strategy.Worker,
	map[domain.StrategyID]SignalRisk,
	[]exchange.Subscription,
	error,
) {
	if len(r.Strategies) == 0 {
		return nil, nil, nil, errors.New("at least one strategy binding is required")
	}
	workers := make(map[domain.InstrumentID]*strategy.Worker, len(r.Strategies))
	risks := make(map[domain.StrategyID]SignalRisk, len(r.Strategies))
	subscriptions := make([]exchange.Subscription, 0, len(r.Strategies))
	for i, binding := range r.Strategies {
		if err := binding.ID.Validate(); err != nil {
			return nil, nil, nil, fmt.Errorf("strategy binding %d ID: %w", i, err)
		}
		if err := binding.InstrumentID.Validate(); err != nil {
			return nil, nil, nil, fmt.Errorf("strategy binding %d instrument: %w", i, err)
		}
		if binding.Worker == nil || binding.Risk == nil {
			return nil, nil, nil, fmt.Errorf("strategy binding %d worker and risk gate are required", i)
		}
		if _, exists := workers[binding.InstrumentID]; exists {
			return nil, nil, nil, fmt.Errorf("instrument %q has multiple strategy bindings", binding.InstrumentID)
		}
		if _, exists := risks[binding.ID]; exists {
			return nil, nil, nil, fmt.Errorf("strategy %q has multiple bindings", binding.ID)
		}
		if err := binding.Subscription.Validate(); err != nil {
			return nil, nil, nil, fmt.Errorf("strategy binding %d subscription: %w", i, err)
		}
		if binding.Subscription.InstrumentID != binding.InstrumentID {
			return nil, nil, nil, fmt.Errorf("strategy %q subscription instrument does not match binding", binding.ID)
		}
		workers[binding.InstrumentID] = binding.Worker
		risks[binding.ID] = binding.Risk
		subscriptions = append(subscriptions, binding.Subscription)
	}
	return workers, risks, subscriptions, nil
}

func (r Runtime) tradingDayKey(strategyID domain.StrategyID) func(time.Time) string {
	for _, binding := range r.Strategies {
		if binding.ID == strategyID {
			return binding.TradingDayKey
		}
	}
	return nil
}

func (r Runtime) strategyIDForInstrument(instrumentID domain.InstrumentID) domain.StrategyID {
	for _, binding := range r.Strategies {
		if binding.InstrumentID == instrumentID {
			return binding.ID
		}
	}
	return ""
}

func (r Runtime) logger() *slog.Logger {
	if r.Logger != nil {
		return r.Logger
	}
	return discardLogger
}
