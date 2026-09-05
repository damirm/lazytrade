package cli

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"os/signal"
	"path/filepath"
	"strings"
	"syscall"
	"time"

	"github.com/damirm/lazytrade/internal/agent"
	appclock "github.com/damirm/lazytrade/internal/clock"
	appconfig "github.com/damirm/lazytrade/internal/config"
	"github.com/damirm/lazytrade/internal/domain"
	"github.com/damirm/lazytrade/internal/exchange"
	"github.com/damirm/lazytrade/internal/exchange/tinvest"
	"github.com/damirm/lazytrade/internal/logging"
	"github.com/damirm/lazytrade/internal/risk"
	"github.com/damirm/lazytrade/internal/storage"
	"github.com/damirm/lazytrade/internal/storage/sqlite"
	"github.com/damirm/lazytrade/internal/strategy"
	"github.com/damirm/lazytrade/internal/strategy/builtin"
	"github.com/spf13/cobra"
)

func newAgentCommand() *cobra.Command {
	var configPath string

	command := &cobra.Command{
		Use:   "agent",
		Short: "Run the trading agent",
		Args:  cobra.NoArgs,
		RunE: func(command *cobra.Command, _ []string) error {
			cfg, err := appconfig.LoadFileFor(configPath, appconfig.CommandAgent)
			if err != nil {
				return err
			}
			runCtx, stop := signal.NotifyContext(command.Context(), os.Interrupt, syscall.SIGTERM)
			defer stop()
			return runAgent(runCtx, configPath, cfg)
		},
	}
	command.Flags().StringVar(&configPath, "config", "", "path to YAML configuration")
	_ = command.MarkFlagRequired("config")
	command.AddCommand(newAgentPreflightCommand())
	command.AddCommand(newAgentHistoryProbeCommand())
	return command
}

func runAgent(ctx context.Context, configPath string, cfg appconfig.Config) error {
	logger, closeLogger, err := logging.Open(cfg.Logging, filepath.Dir(configPath))
	if err != nil {
		return err
	}
	defer closeLogger()

	if cfg.Agent.EmergencyStop {
		return errors.New("agent startup blocked by emergency_stop")
	}
	if len(cfg.Agent.Strategies) == 0 {
		return errors.New("agent requires at least one strategy")
	}
	strategyConfig := cfg.Agent.Strategies[0]
	for _, configured := range cfg.Agent.Strategies[1:] {
		if configured.Exchange != strategyConfig.Exchange {
			return errors.New("live multi-strategy agent currently requires all strategies to use one exchange")
		}
	}
	exchangeConfig := cfg.Exchanges[strategyConfig.Exchange]
	if !exchangeConfig.Sandbox || exchangeConfig.AllowLiveTrading {
		return errors.New("agent currently supports T-Invest sandbox mode only")
	}
	for _, configured := range cfg.Agent.Strategies {
		if configured.Execution.OrderType != "market" {
			return fmt.Errorf("strategy %q: moving_average_cross live execution currently supports market orders only", configured.ID)
		}
		if configured.Risk.MaxDailyLoss != nil &&
			configured.Risk.MaxDailyLoss.Action != "pause" {
			return fmt.Errorf("strategy %q: live agent currently supports max_daily_loss.action=pause only", configured.ID)
		}
	}
	if cfg.Database.Driver != "sqlite" {
		return fmt.Errorf("agent storage: unsupported database driver %q", cfg.Database.Driver)
	}

	dsn, err := configuredValue(cfg.Database.DSN, cfg.Database.DSNEnv, "database DSN")
	if err != nil {
		return err
	}
	if dsn != ":memory:" && !filepath.IsAbs(dsn) {
		dsn = filepath.Join(filepath.Dir(configPath), dsn)
	}
	store, err := sqlite.Open(ctx, dsn)
	if err != nil {
		return fmt.Errorf("open agent storage: %w", err)
	}
	defer store.Close()
	if err := store.Acquire(ctx, "lazytrade-agent"); err != nil {
		return fmt.Errorf("acquire agent storage lock: %w", err)
	}

	token, err := requiredEnvironment(exchangeConfig.TokenEnv)
	if err != nil {
		return err
	}
	accountID, err := requiredEnvironment(exchangeConfig.AccountIDEnv)
	if err != nil {
		return err
	}
	adapter, err := tinvest.Open(ctx, tinvest.Config{
		Name:       strategyConfig.Exchange,
		Token:      token,
		AccountID:  accountID,
		CACertPath: resolveConfigPath(configPath, exchangeConfig.CACertPath),
	})
	if err != nil {
		return fmt.Errorf("open T-Invest: %w", err)
	}
	defer adapter.Close()

	workers := make(map[domain.InstrumentID]*strategy.Worker, len(cfg.Agent.Strategies))
	strategyIDs := make(map[domain.InstrumentID]domain.StrategyID, len(cfg.Agent.Strategies))
	riskGates := make(map[domain.StrategyID]agent.SignalRisk, len(cfg.Agent.Strategies))
	tradingDayKeys := make(map[domain.StrategyID]func(time.Time) string, len(cfg.Agent.Strategies))
	subscriptions := make([]exchange.Subscription, 0, len(cfg.Agent.Strategies))
	now := time.Now().UTC()
	for _, configured := range cfg.Agent.Strategies {
		requestedInstrumentID := domain.InstrumentID(configured.Instrument)
		instrument, instrumentErr := adapter.Instrument(ctx, requestedInstrumentID)
		if instrumentErr != nil {
			return fmt.Errorf("strategy %q instrument: %w", configured.ID, instrumentErr)
		}
		instrumentID := instrument.ID
		if owner := strategyIDs[instrumentID]; owner != "" {
			return fmt.Errorf("strategies %q and %q resolve to the same instrument %q", owner, configured.ID, instrumentID)
		}
		interval, intervalErr := builtin.CandleInterval(configured)
		if intervalErr != nil {
			return fmt.Errorf("strategy %q interval: %w", configured.ID, intervalErr)
		}
		implementation, implementationErr := builtin.Build(configured)
		if implementationErr != nil {
			return fmt.Errorf("build strategy %q: %w", configured.ID, implementationErr)
		}
		definitionPayload, marshalErr := json.Marshal(configured)
		if marshalErr != nil {
			return marshalErr
		}
		definitionHash := sha256.Sum256(definitionPayload)
		strategyID := domain.StrategyID(configured.ID)
		if registerErr := store.RegisterStrategy(ctx, storage.StrategyDefinition{
			ID: strategyID, ExchangeAccountID: domain.ExchangeAccountID(configured.Exchange),
			InstrumentID: instrumentID, StrategyType: implementation.Type(),
			ConfigHash: hex.EncodeToString(definitionHash[:]), CreatedAt: now, UpdatedAt: now,
		}); registerErr != nil {
			return fmt.Errorf("register strategy %q: %w", configured.ID, registerErr)
		}
		statePort, stateErr := strategy.NewDurableStatePort(store, implementation.Type())
		if stateErr != nil {
			return fmt.Errorf("build strategy state port %q: %w", configured.ID, stateErr)
		}
		worker, workerErr := strategy.NewWorker(
			strategyID, domain.ExchangeAccountID(configured.Exchange),
			instrumentID, implementation, statePort,
		)
		if workerErr != nil {
			return fmt.Errorf("build strategy worker %q: %w", configured.ID, workerErr)
		}
		policy, policyErr := risk.NewTradingDayPolicy(configured.TradingDay.Timezone, configured.TradingDay.ResetAt)
		if policyErr != nil {
			return fmt.Errorf("strategy %q trading day: %w", configured.ID, policyErr)
		}
		riskConfig, configErr := buildLiveRiskConfig(configured, instrument.SettlementAsset, policy)
		if configErr != nil {
			return fmt.Errorf("strategy %q risk: %w", configured.ID, configErr)
		}
		riskGate, gateErr := agent.NewPersistentRiskGate(
			riskConfig, store, appclock.NewVirtual(time.Unix(0, 0).UTC()),
		)
		if gateErr != nil {
			return fmt.Errorf("build persistent risk gate %q: %w", configured.ID, gateErr)
		}
		workers[instrumentID] = worker
		strategyIDs[instrumentID] = strategyID
		riskGates[strategyID] = riskGate
		tradingDayKeys[strategyID] = func(at time.Time) string { return policy.At(at).Key }
		subscriptions = append(subscriptions, exchange.Subscription{
			InstrumentID: instrumentID, Kind: exchange.SubscriptionCandles, Interval: interval,
		})
	}
	runtime := agent.Runtime{
		Logger:         logger,
		Exchange:       adapter,
		Workers:        workers,
		StrategyIDs:    strategyIDs,
		Risks:          riskGates,
		Intents:        store,
		Lifecycle:      store,
		Subscriptions:  subscriptions,
		TradingDayKeys: tradingDayKeys,
		Reconciler:     agent.Reconciler{Exchange: adapter, Store: store},
		HistorySource:  tinvest.ExecutionHistorySource,
		// Sandbox bootstrap is deliberately bounded. Production will require an
		// explicit bootstrap_from before live trading is enabled.
		HistoryBootstrap:       24 * time.Hour,
		HistoryOverlap:         15 * time.Minute,
		HistoryVisibilityDelay: 5 * time.Minute,
	}
	if err := runtime.Run(ctx); err != nil && !errors.Is(err, context.Canceled) {
		return err
	}
	return nil
}

func buildLiveRiskConfig(
	strategyConfig appconfig.StrategyConfig,
	settlementAsset string,
	policy risk.TradingDayPolicy,
) (risk.Config, error) {
	result := risk.Config{
		StrategyID:      domain.StrategyID(strategyConfig.ID),
		SettlementAsset: settlementAsset, TradingDay: policy,
	}
	if configured := strategyConfig.Risk.MaxPositionValue; configured != nil {
		value, err := domain.NewMoney(configured.Amount, configured.Asset)
		if err != nil {
			return result, err
		}
		result.MaxPositionValue = &value
	}
	if configured := strategyConfig.Risk.MaxDailyLoss; configured != nil {
		value, err := domain.NewMoney(configured.Amount, configured.Asset)
		if err != nil {
			return result, err
		}
		result.MaxDailyLoss = &risk.DailyLossLimit{
			Limit: value, Mode: risk.PnLMode(configured.PnL),
		}
	}
	return result, nil
}

func configuredValue(literal, environment, label string) (string, error) {
	if environment != "" {
		return requiredEnvironment(environment)
	}
	if strings.TrimSpace(literal) == "" {
		return "", fmt.Errorf("%s is not configured", label)
	}
	return literal, nil
}

func requiredEnvironment(name string) (string, error) {
	value, ok := os.LookupEnv(name)
	if !ok || strings.TrimSpace(value) == "" {
		return "", fmt.Errorf("required environment variable %q is not set", name)
	}
	return value, nil
}
