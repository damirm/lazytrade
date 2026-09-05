package cli

import (
	"context"
	"errors"
	"fmt"
	"path/filepath"
	"time"

	appconfig "github.com/damirm/lazytrade/internal/config"
	"github.com/damirm/lazytrade/internal/domain"
	"github.com/damirm/lazytrade/internal/exchange"
	"github.com/damirm/lazytrade/internal/exchange/tinvest"
	"github.com/damirm/lazytrade/internal/storage/sqlite"
	"github.com/damirm/lazytrade/internal/strategy/builtin"
	"github.com/shopspring/decimal"
	"github.com/spf13/cobra"
)

type agentPreflightReport struct {
	ExchangeID      string
	AccountID       string
	AccountName     string
	AccountStatus   string
	AccessLevel     string
	Instruments     []agentPreflightInstrument
	PortfolioValues []domain.Money
	Positions       int
	OpenOrders      int
}

type agentPreflightInstrument struct {
	StrategyID      string
	InstrumentID    domain.InstrumentID
	Symbol          string
	LotSize         decimal.Decimal
	Quantity        decimal.Decimal
	SettlementAsset string
}

func newAgentPreflightCommand() *cobra.Command {
	var configPath string
	var timeout time.Duration
	command := &cobra.Command{
		Use:   "preflight",
		Short: "Check sandbox readiness without running strategies or placing orders",
		Args:  cobra.NoArgs,
		RunE: func(command *cobra.Command, _ []string) error {
			cfg, err := appconfig.LoadFileFor(configPath, appconfig.CommandAgent)
			if err != nil {
				return err
			}
			ctx, cancel := context.WithTimeout(command.Context(), timeout)
			defer cancel()
			report, err := runAgentPreflight(ctx, configPath, cfg)
			if err != nil {
				return err
			}
			out := command.OutOrStdout()
			_, _ = fmt.Fprintf(out, "STATUS\tOK\nEXCHANGE\t%s\nACCOUNT_ID\t%s\nACCOUNT_NAME\t%s\nACCOUNT_STATUS\t%s\nACCESS\t%s\n",
				report.ExchangeID, report.AccountID, report.AccountName, report.AccountStatus, report.AccessLevel)
			for _, instrument := range report.Instruments {
				_, _ = fmt.Fprintf(out, "STRATEGY\t%s\nINSTRUMENT\t%s\nSYMBOL\t%s\nLOT_SIZE\t%s\nCONFIGURED_QUANTITY\t%s\nSETTLEMENT_ASSET\t%s\n",
					instrument.StrategyID, instrument.InstrumentID, instrument.Symbol,
					instrument.LotSize, instrument.Quantity, instrument.SettlementAsset)
			}
			for _, value := range report.PortfolioValues {
				_, _ = fmt.Fprintf(out, "PORTFOLIO_VALUE\t%s %s\n", value.Amount, value.Asset)
			}
			_, _ = fmt.Fprintf(out, "POSITIONS\t%d\nOPEN_ORDERS\t%d\nMARKET_STREAM\tOK\nEXECUTION_STREAM\tOK\nSQLITE\tOK\n",
				report.Positions, report.OpenOrders)
			return nil
		},
	}
	command.Flags().StringVar(&configPath, "config", "", "path to YAML configuration")
	command.Flags().DurationVar(&timeout, "timeout", 20*time.Second, "overall preflight timeout")
	_ = command.MarkFlagRequired("config")
	return command
}

func runAgentPreflight(ctx context.Context, configPath string, cfg appconfig.Config) (agentPreflightReport, error) {
	var report agentPreflightReport
	if cfg.Agent.EmergencyStop {
		return report, errors.New("preflight blocked by emergency_stop")
	}
	if len(cfg.Agent.Strategies) == 0 {
		return report, errors.New("preflight requires at least one strategy")
	}
	strategyConfig := cfg.Agent.Strategies[0]
	for _, configured := range cfg.Agent.Strategies[1:] {
		if configured.Exchange != strategyConfig.Exchange {
			return report, errors.New("preflight currently requires all strategies to use one exchange")
		}
	}
	exchangeConfig := cfg.Exchanges[strategyConfig.Exchange]
	if !exchangeConfig.Sandbox || exchangeConfig.AllowLiveTrading {
		return report, errors.New("preflight currently supports T-Invest sandbox mode only")
	}
	if cfg.Database.Driver != "sqlite" {
		return report, fmt.Errorf("preflight storage: unsupported database driver %q", cfg.Database.Driver)
	}
	dsn, err := configuredValue(cfg.Database.DSN, cfg.Database.DSNEnv, "database DSN")
	if err != nil {
		return report, err
	}
	if dsn == ":memory:" {
		return report, errors.New("preflight requires file-backed SQLite to verify the agent lock")
	}
	if !filepath.IsAbs(dsn) {
		dsn = filepath.Join(filepath.Dir(configPath), dsn)
	}
	store, err := sqlite.Open(ctx, dsn)
	if err != nil {
		return report, fmt.Errorf("preflight SQLite: %w", err)
	}
	defer store.Close()
	if err := store.Acquire(ctx, "lazytrade-preflight"); err != nil {
		return report, fmt.Errorf("preflight SQLite lock: %w", err)
	}

	token, err := requiredEnvironment(exchangeConfig.TokenEnv)
	if err != nil {
		return report, err
	}
	accountID, err := requiredEnvironment(exchangeConfig.AccountIDEnv)
	if err != nil {
		return report, err
	}
	adapter, err := tinvest.Open(ctx, tinvest.Config{
		Name: strategyConfig.Exchange, Token: token, AccountID: accountID,
		CACertPath: resolveConfigPath(configPath, exchangeConfig.CACertPath),
	})
	if err != nil {
		return report, fmt.Errorf("preflight connect: %w", err)
	}
	defer adapter.Close()

	account, err := findSandboxAccount(ctx, adapter, accountID)
	if err != nil {
		return report, err
	}
	if account.Status != "OPEN" {
		return report, fmt.Errorf("sandbox account status is %s, expected OPEN", account.Status)
	}
	if account.AccessLevel != "FULL_ACCESS" {
		return report, fmt.Errorf("sandbox account access is %s, expected FULL_ACCESS", account.AccessLevel)
	}
	alias := domain.ExchangeAccountID(strategyConfig.Exchange)
	instruments := make([]agentPreflightInstrument, 0, len(cfg.Agent.Strategies))
	subscriptions := make([]exchange.Subscription, 0, len(cfg.Agent.Strategies))
	owners := make(map[domain.InstrumentID]string, len(cfg.Agent.Strategies))
	for _, configured := range cfg.Agent.Strategies {
		instrument, instrumentErr := adapter.Instrument(ctx, domain.InstrumentID(configured.Instrument))
		if instrumentErr != nil {
			return report, fmt.Errorf("preflight strategy %q instrument: %w", configured.ID, instrumentErr)
		}
		if owner := owners[instrument.ID]; owner != "" {
			return report, fmt.Errorf("strategies %q and %q resolve to the same instrument %q", owner, configured.ID, instrument.ID)
		}
		owners[instrument.ID] = configured.ID
		quantity, quantityErr := domain.NewQuantity(configured.Execution.Quantity)
		if quantityErr != nil {
			return report, fmt.Errorf("preflight strategy %q quantity: %w", configured.ID, quantityErr)
		}
		if wholeLotsErr := validateWholeLots(quantity, instrument.QuantityStep); wholeLotsErr != nil {
			return report, fmt.Errorf("preflight strategy %q: %w", configured.ID, wholeLotsErr)
		}
		interval, intervalErr := builtin.CandleInterval(configured)
		if intervalErr != nil {
			return report, fmt.Errorf("preflight strategy %q interval: %w", configured.ID, intervalErr)
		}
		instruments = append(instruments, agentPreflightInstrument{
			StrategyID: configured.ID, InstrumentID: instrument.ID, Symbol: instrument.Symbol,
			LotSize: instrument.QuantityStep.Value, Quantity: quantity.Value,
			SettlementAsset: instrument.SettlementAsset,
		})
		subscriptions = append(subscriptions, exchange.Subscription{
			InstrumentID: instrument.ID, Kind: exchange.SubscriptionCandles, Interval: interval,
		})
	}
	portfolio, err := adapter.Portfolio(ctx, alias)
	if err != nil {
		return report, fmt.Errorf("preflight portfolio: %w", err)
	}
	orders, err := adapter.OpenOrders(ctx, alias)
	if err != nil {
		return report, fmt.Errorf("preflight open orders: %w", err)
	}
	executionCtx, stopExecutions := context.WithCancel(ctx)
	executionStream, err := adapter.SubscribeExecutions(executionCtx, alias)
	if err != nil {
		stopExecutions()
		return report, fmt.Errorf("preflight execution stream: %w", err)
	}
	marketCtx, stopMarket := context.WithCancel(ctx)
	marketStream, err := adapter.SubscribeMarketData(marketCtx, subscriptions)
	if err != nil {
		stopMarket()
		stopExecutions()
		return report, fmt.Errorf("preflight market stream: %w", err)
	}
	if err := waitForHealthyStreams(ctx, marketStream, executionStream); err != nil {
		stopMarket()
		stopExecutions()
		return report, err
	}
	stopMarket()
	stopExecutions()
	return agentPreflightReport{
		ExchangeID: strategyConfig.Exchange, AccountID: account.ID,
		AccountName: account.Name, AccountStatus: account.Status, AccessLevel: account.AccessLevel,
		Instruments:     instruments,
		PortfolioValues: portfolio.TotalValue, Positions: len(portfolio.Positions), OpenOrders: len(orders),
	}, nil
}

func findSandboxAccount(ctx context.Context, adapter *tinvest.Adapter, accountID string) (tinvest.SandboxAccount, error) {
	accounts, err := adapter.SandboxAccounts(ctx)
	if err != nil {
		return tinvest.SandboxAccount{}, fmt.Errorf("preflight accounts: %w", err)
	}
	for _, account := range accounts {
		if account.ID == accountID {
			return account, nil
		}
	}
	return tinvest.SandboxAccount{}, fmt.Errorf("configured sandbox account %q was not found", accountID)
}

func validateWholeLots(quantity, lotSize domain.Quantity) error {
	if err := lotSize.Validate(); err != nil || !lotSize.Value.IsPositive() {
		return errors.New("instrument returned an invalid lot size")
	}
	lots := quantity.Value.Div(lotSize.Value)
	if !quantity.Value.IsPositive() || !lots.Equal(lots.Truncate(0)) {
		return fmt.Errorf("configured quantity %s is not a whole number of lots; instrument lot size is %s",
			quantity.Value, lotSize.Value)
	}
	return nil
}

func waitForHealthyStreams(ctx context.Context, market exchange.MarketStream, executions exchange.ExecutionStream) error {
	for {
		select {
		case <-ctx.Done():
			return fmt.Errorf("preflight streams: %w", ctx.Err())
		case err, ok := <-market.Errors:
			if ok && err != nil {
				return fmt.Errorf("preflight market stream: %w", err)
			}
		case err, ok := <-executions.Errors:
			if ok && err != nil {
				return fmt.Errorf("preflight execution stream: %w", err)
			}
		case state, ok := <-market.State:
			if !ok {
				return errors.New("preflight market stream closed before becoming healthy")
			}
			if state.State == exchange.StreamHealthy {
				return nil
			}
		}
	}
}
