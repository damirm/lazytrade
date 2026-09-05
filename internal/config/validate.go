package config

import (
	"fmt"
	"os"
	"regexp"
	"strconv"
	"strings"
	"time"
)

type Command string

const (
	CommandAgent    Command = "agent"
	CommandTerminal Command = "terminal"
	CommandBacktest Command = "backtest"
)

var decimalPattern = regexp.MustCompile(`^[+-]?(?:0|[1-9][0-9]*)(?:\.[0-9]+)?$`)

// Validate performs all validation which does not require network access or
// resolving environment variables.
func (cfg Config) Validate() error {
	if cfg.Version != CurrentVersion {
		return fmt.Errorf("config.version: unsupported schema version %d (expected %d)", cfg.Version, CurrentVersion)
	}
	if cfg.Logging.Level != "" && cfg.Logging.Level != "debug" && cfg.Logging.Level != "info" && cfg.Logging.Level != "warn" && cfg.Logging.Level != "error" {
		return fmt.Errorf("config.logging.level: unsupported level %q", cfg.Logging.Level)
	}
	if cfg.Logging.Format != "" && cfg.Logging.Format != "json" && cfg.Logging.Format != "text" {
		return fmt.Errorf("config.logging.format: unsupported format %q", cfg.Logging.Format)
	}
	for id, exchange := range cfg.Exchanges {
		path := "config.exchanges." + id
		if strings.TrimSpace(id) == "" {
			return fmt.Errorf("config.exchanges: exchange ID must not be empty")
		}
		if exchange.Type != "tinvest" {
			return fmt.Errorf("%s.type: unsupported exchange type %q", path, exchange.Type)
		}
		if exchange.AllowLiveTrading && exchange.Sandbox {
			return fmt.Errorf("%s.allow_live_trading: cannot enable live trading in sandbox mode", path)
		}
	}

	strategies := make(map[string]StrategyConfig, len(cfg.Agent.Strategies))
	instruments := make(map[string]string, len(cfg.Agent.Strategies))
	for i, strategy := range cfg.Agent.Strategies {
		path := fmt.Sprintf("config.agent.strategies[%d]", i)
		if strategy.ID == "" {
			return fmt.Errorf("%s.id: must not be empty", path)
		}
		if _, exists := strategies[strategy.ID]; exists {
			return fmt.Errorf("%s.id: duplicate strategy ID %q", path, strategy.ID)
		}
		strategies[strategy.ID] = strategy
		if _, exists := cfg.Exchanges[strategy.Exchange]; !exists {
			return fmt.Errorf("%s.exchange: exchange %q is not defined", path, strategy.Exchange)
		}
		if strategy.Instrument == "" {
			return fmt.Errorf("%s.instrument: must not be empty", path)
		}
		ownershipKey := strategy.Exchange + "\x00" + strategy.Instrument
		if owner, exists := instruments[ownershipKey]; exists {
			return fmt.Errorf("%s.instrument: exchange/instrument is already used by strategy %q", path, owner)
		}
		instruments[ownershipKey] = strategy.ID
		if err := validateStrategy(path, strategy); err != nil {
			return err
		}
	}

	if cfg.Terminal.RefreshInterval != "" {
		if err := positiveDuration(cfg.Terminal.RefreshInterval); err != nil {
			return fmt.Errorf("config.terminal.refresh_interval: %w", err)
		}
	}
	for i, tab := range cfg.Terminal.Tabs {
		path := fmt.Sprintf("config.terminal.tabs[%d]", i)
		if tab.Title == "" {
			return fmt.Errorf("%s.title: must not be empty", path)
		}
		if _, exists := cfg.Exchanges[tab.Exchange]; !exists {
			return fmt.Errorf("%s.exchange: exchange %q is not defined", path, tab.Exchange)
		}
		if tab.Instrument == "" {
			return fmt.Errorf("%s.instrument: must not be empty", path)
		}
		for j, panel := range tab.Panels {
			if err := validatePanel(fmt.Sprintf("%s.panels[%d]", path, j), panel); err != nil {
				return err
			}
		}
	}

	runIDs := make(map[string]struct{}, len(cfg.Backtest.Runs))
	for i, run := range cfg.Backtest.Runs {
		path := fmt.Sprintf("config.backtest.runs[%d]", i)
		if run.ID == "" {
			return fmt.Errorf("%s.id: must not be empty", path)
		}
		if _, exists := runIDs[run.ID]; exists {
			return fmt.Errorf("%s.id: duplicate backtest run ID %q", path, run.ID)
		}
		runIDs[run.ID] = struct{}{}
		if _, exists := strategies[run.Strategy]; !exists {
			return fmt.Errorf("%s.strategy: strategy %q is not defined", path, run.Strategy)
		}
		if err := validateBacktestRun(path, run); err != nil {
			return err
		}
	}
	return nil
}

func validateStrategy(path string, strategy StrategyConfig) error {
	params := strategy.Strategy.Params
	if err := positiveDuration(params.CandleInterval); err != nil {
		return fmt.Errorf("%s.strategy.params.candle_interval: %w", path, err)
	}
	switch strategy.Strategy.Type {
	case "moving_average_cross":
		if params.FastPeriod <= 0 {
			return fmt.Errorf("%s.strategy.params.fast_period: must be positive", path)
		}
		if params.SlowPeriod <= params.FastPeriod {
			return fmt.Errorf("%s.strategy.params.slow_period: must be greater than fast_period", path)
		}
		if params.DayOfMonth != 0 || params.Time != "" || params.Timezone != "" {
			return fmt.Errorf("%s.strategy.params: periodic schedule fields are not allowed for moving_average_cross", path)
		}
	case "periodic_investment":
		if params.FastPeriod != 0 || params.SlowPeriod != 0 {
			return fmt.Errorf("%s.strategy.params: moving-average periods are not allowed for periodic_investment", path)
		}
		if params.DayOfMonth < 1 || params.DayOfMonth > 28 {
			return fmt.Errorf("%s.strategy.params.day_of_month: must be between 1 and 28", path)
		}
		if _, err := time.Parse("15:04", params.Time); err != nil {
			return fmt.Errorf("%s.strategy.params.time: must use HH:MM format", path)
		}
		if _, err := time.LoadLocation(params.Timezone); err != nil {
			return fmt.Errorf("%s.strategy.params.timezone: invalid timezone %q", path, params.Timezone)
		}
		if strategy.Execution.OrderType != "market" {
			return fmt.Errorf("%s.execution.order_type: periodic_investment supports only market orders", path)
		}
	default:
		return fmt.Errorf("%s.strategy.type: unsupported strategy type %q", path, strategy.Strategy.Type)
	}
	if err := positiveDecimal(strategy.Execution.Quantity); err != nil {
		return fmt.Errorf("%s.execution.quantity: %w", path, err)
	}
	if strategy.Execution.OrderType != "market" && strategy.Execution.OrderType != "limit" {
		return fmt.Errorf("%s.execution.order_type: unsupported order type %q", path, strategy.Execution.OrderType)
	}
	if _, err := time.LoadLocation(strategy.TradingDay.Timezone); err != nil {
		return fmt.Errorf("%s.trading_day.timezone: invalid timezone %q", path, strategy.TradingDay.Timezone)
	}
	if _, err := time.Parse("15:04", strategy.TradingDay.ResetAt); err != nil {
		return fmt.Errorf("%s.trading_day.reset_at: must use HH:MM format", path)
	}
	if strategy.Risk.MaxDailyLoss != nil {
		limit := strategy.Risk.MaxDailyLoss
		if err := validateMoney(path+".risk.max_daily_loss", limit.Amount, limit.Asset); err != nil {
			return err
		}
		if limit.PnL != "realized" && limit.PnL != "total" {
			return fmt.Errorf("%s.risk.max_daily_loss.pnl: unsupported P&L mode %q", path, limit.PnL)
		}
		if limit.Action != "pause" && limit.Action != "close_and_pause" {
			return fmt.Errorf("%s.risk.max_daily_loss.action: unsupported action %q", path, limit.Action)
		}
	}
	if value := strategy.Risk.MaxPositionValue; value != nil {
		if err := validateMoney(path+".risk.max_position_value", value.Amount, value.Asset); err != nil {
			return err
		}
	}
	return nil
}

func validatePanel(path string, panel TerminalPanel) error {
	switch panel.Type {
	case "chart":
		switch panel.Mode {
		case "candles", "line", "time_series", "sparkline", "volume":
		default:
			return fmt.Errorf("%s.mode: unsupported chart mode %q", path, panel.Mode)
		}
		if err := positiveDuration(panel.Interval); err != nil {
			return fmt.Errorf("%s.interval: %w", path, err)
		}
		if panel.History <= 0 {
			return fmt.Errorf("%s.history: must be positive", path)
		}
		for i, overlay := range panel.Overlays {
			if overlay.Type != "sma" {
				return fmt.Errorf("%s.overlays[%d].type: unsupported overlay type %q", path, i, overlay.Type)
			}
			if overlay.Period <= 0 {
				return fmt.Errorf("%s.overlays[%d].period: must be positive", path, i)
			}
		}
	case "order_book":
		if panel.Depth <= 0 {
			return fmt.Errorf("%s.depth: must be positive", path)
		}
	case "trades":
		if panel.Limit <= 0 {
			return fmt.Errorf("%s.limit: must be positive", path)
		}
	default:
		return fmt.Errorf("%s.type: unsupported panel type %q", path, panel.Type)
	}
	return nil
}

func validateBacktestRun(path string, run BacktestRun) error {
	if run.Data.Type != "csv" {
		return fmt.Errorf("%s.data.type: unsupported data type %q", path, run.Data.Type)
	}
	if run.Data.Path == "" {
		return fmt.Errorf("%s.data.path: must not be empty", path)
	}
	if run.Data.Interval == "" && run.Data.MetadataPath == "" {
		return fmt.Errorf("%s.data.interval: must not be empty when metadata_path is not set", path)
	}
	if run.Data.Interval != "" {
		if err := positiveDuration(run.Data.Interval); err != nil {
			return fmt.Errorf("%s.data.interval: %w", path, err)
		}
	}
	if run.Data.MetadataPath == "" {
		if run.Data.PriceAsset == "" || run.Data.PriceAsset != strings.ToUpper(run.Data.PriceAsset) {
			return fmt.Errorf("%s.data.price_asset: must be a non-empty uppercase asset when metadata_path is not set", path)
		}
		if err := positiveDecimal(run.Data.TickSize); err != nil {
			return fmt.Errorf("%s.data.tick_size: %w", path, err)
		}
		if err := positiveDecimal(run.Data.LotSize); err != nil {
			return fmt.Errorf("%s.data.lot_size: %w", path, err)
		}
	} else {
		if run.Data.PriceAsset != "" && run.Data.PriceAsset != strings.ToUpper(run.Data.PriceAsset) {
			return fmt.Errorf("%s.data.price_asset: must be uppercase", path)
		}
		if run.Data.TickSize != "" {
			if err := positiveDecimal(run.Data.TickSize); err != nil {
				return fmt.Errorf("%s.data.tick_size: %w", path, err)
			}
		}
		if run.Data.LotSize != "" {
			if err := positiveDecimal(run.Data.LotSize); err != nil {
				return fmt.Errorf("%s.data.lot_size: %w", path, err)
			}
		}
	}
	if run.Data.Timezone == "" && run.Data.MetadataPath == "" {
		return fmt.Errorf("%s.data.timezone: must not be empty when metadata_path is not set", path)
	}
	if run.Data.Timezone != "" {
		if _, err := time.LoadLocation(run.Data.Timezone); err != nil {
			return fmt.Errorf("%s.data.timezone: invalid timezone %q", path, run.Data.Timezone)
		}
	}
	if run.Data.GapPolicy != "fail" && run.Data.GapPolicy != "allow" && run.Data.GapPolicy != "mark" {
		return fmt.Errorf("%s.data.gap_policy: unsupported gap policy %q", path, run.Data.GapPolicy)
	}
	if err := validateMoney(path+".execution.initial_cash", run.Execution.InitialCash.Amount, run.Execution.InitialCash.Asset); err != nil {
		return err
	}
	if run.Data.PriceAsset != "" && run.Data.PriceAsset != run.Execution.InitialCash.Asset {
		return fmt.Errorf("%s.data.price_asset: must match execution.initial_cash.asset for the single-asset broker", path)
	}
	if run.Execution.Commission.Type != "percent" {
		return fmt.Errorf("%s.execution.commission.type: unsupported commission type %q", path, run.Execution.Commission.Type)
	}
	if err := nonNegativeDecimal(run.Execution.Commission.Value); err != nil {
		return fmt.Errorf("%s.execution.commission.value: %w", path, err)
	}
	if run.Execution.Slippage.Type != "basis_points" {
		return fmt.Errorf("%s.execution.slippage.type: unsupported slippage type %q", path, run.Execution.Slippage.Type)
	}
	if err := nonNegativeDecimal(run.Execution.Slippage.Value); err != nil {
		return fmt.Errorf("%s.execution.slippage.value: %w", path, err)
	}
	if run.Execution.MarketFill != "next_open" {
		return fmt.Errorf("%s.execution.market_fill: unsupported market fill %q", path, run.Execution.MarketFill)
	}
	if run.Execution.LimitFill != "touch" {
		return fmt.Errorf("%s.execution.limit_fill: unsupported limit fill %q", path, run.Execution.LimitFill)
	}
	if run.Output.Directory == "" {
		return fmt.Errorf("%s.output.directory: must not be empty", path)
	}
	if !run.Output.JSON && !run.Output.TradesCSV {
		return fmt.Errorf("%s.output: at least one output format must be enabled", path)
	}
	return nil
}

func validateMoney(path, amount, asset string) error {
	if err := positiveDecimal(amount); err != nil {
		return fmt.Errorf("%s.amount: %w", path, err)
	}
	if asset == "" {
		return fmt.Errorf("%s.asset: must not be empty", path)
	}
	if asset != strings.ToUpper(asset) {
		return fmt.Errorf("%s.asset: must be uppercase", path)
	}
	return nil
}

func positiveDuration(value string) error {
	duration, err := time.ParseDuration(value)
	if err != nil {
		return fmt.Errorf("invalid duration %q", value)
	}
	if duration <= 0 {
		return fmt.Errorf("must be positive")
	}
	return nil
}

func positiveDecimal(value string) error {
	if err := nonNegativeDecimal(value); err != nil {
		return err
	}
	if isDecimalZero(value) {
		return fmt.Errorf("must be positive")
	}
	return nil
}

func nonNegativeDecimal(value string) error {
	if !decimalPattern.MatchString(value) {
		return fmt.Errorf("invalid decimal %q", value)
	}
	if strings.HasPrefix(value, "-") && !isDecimalZero(value) {
		return fmt.Errorf("must not be negative")
	}
	return nil
}

func isDecimalZero(value string) bool {
	value = strings.TrimPrefix(value, "+")
	value = strings.TrimPrefix(value, "-")
	value = strings.ReplaceAll(value, ".", "")
	_, err := strconv.ParseUint(value, 10, 64)
	if err == nil {
		return strings.Trim(value, "0") == ""
	}
	return strings.Trim(value, "0") == ""
}

// ValidateFor resolves only the environment variables needed by command.
// A nil lookup uses os.LookupEnv. Backtest deliberately resolves no secrets.
func (cfg Config) ValidateFor(command Command, lookup func(string) (string, bool)) error {
	if err := cfg.Validate(); err != nil {
		return err
	}
	if lookup == nil {
		lookup = os.LookupEnv
	}
	var required []credentialReference
	switch command {
	case CommandAgent:
		if len(cfg.Agent.Strategies) == 0 {
			return fmt.Errorf("config.agent.strategies: at least one strategy is required for agent")
		}
		for _, strategy := range cfg.Agent.Strategies {
			exchange := cfg.Exchanges[strategy.Exchange]
			path := "config.exchanges." + strategy.Exchange
			required = addExchangeCredentials(required, exchange, path, true)
		}
	case CommandTerminal:
		if len(cfg.Terminal.Tabs) == 0 {
			return fmt.Errorf("config.terminal.tabs: at least one tab is required for terminal")
		}
		for _, tab := range cfg.Terminal.Tabs {
			exchange := cfg.Exchanges[tab.Exchange]
			path := "config.exchanges." + tab.Exchange
			required = addExchangeCredentials(required, exchange, path, false)
		}
	case CommandBacktest:
		if len(cfg.Backtest.Runs) == 0 {
			return fmt.Errorf("config.backtest.runs: at least one run is required for backtest")
		}
		return nil
	default:
		return fmt.Errorf("config: unsupported validation command %q", command)
	}
	for _, reference := range required {
		if reference.name == "" {
			return fmt.Errorf("%s: credential environment variable name must not be empty", reference.path)
		}
		if value, ok := lookup(reference.name); !ok || value == "" {
			return fmt.Errorf("%s: required environment variable %q is not set", reference.path, reference.name)
		}
	}
	return nil
}

type credentialReference struct {
	name string
	path string
}

func addExchangeCredentials(required []credentialReference, exchange ExchangeConfig, path string, account bool) []credentialReference {
	required = appendCredential(required, credentialReference{exchange.TokenEnv, path + ".token_env"})
	if account {
		required = appendCredential(required, credentialReference{exchange.AccountIDEnv, path + ".account_id_env"})
	}
	return required
}

func appendCredential(required []credentialReference, candidate credentialReference) []credentialReference {
	for _, existing := range required {
		if existing.name == candidate.name && existing.path == candidate.path {
			return required
		}
	}
	return append(required, candidate)
}
