package config

const CurrentVersion = 1

// Config is schema version 1 of the application configuration.
type Config struct {
	Version   int                       `yaml:"version"`
	Database  DatabaseConfig            `yaml:"database,omitempty"`
	Logging   LoggingConfig             `yaml:"logging,omitempty"`
	Exchanges map[string]ExchangeConfig `yaml:"exchanges,omitempty"`
	Agent     AgentConfig               `yaml:"agent,omitempty"`
	Terminal  TerminalConfig            `yaml:"terminal,omitempty"`
	Backtest  BacktestConfig            `yaml:"backtest,omitempty"`
}

type DatabaseConfig struct {
	Driver string `yaml:"driver,omitempty"`
	DSN    string `yaml:"dsn,omitempty"`
	DSNEnv string `yaml:"dsn_env,omitempty"`
}

type LoggingConfig struct {
	Level  string `yaml:"level,omitempty"`
	Format string `yaml:"format,omitempty"`
	Output string `yaml:"output,omitempty"`
}

type ExchangeConfig struct {
	Type             string `yaml:"type"`
	TokenEnv         string `yaml:"token_env,omitempty"`
	AccountIDEnv     string `yaml:"account_id_env,omitempty"`
	CACertPath       string `yaml:"ca_cert_path,omitempty"`
	Sandbox          bool   `yaml:"sandbox,omitempty"`
	AllowLiveTrading bool   `yaml:"allow_live_trading,omitempty"`
}

type AgentConfig struct {
	Web           WebConfig        `yaml:"web,omitempty"`
	EmergencyStop bool             `yaml:"emergency_stop,omitempty"`
	Strategies    []StrategyConfig `yaml:"strategies,omitempty"`
}

type WebConfig struct {
	Listen       string `yaml:"listen,omitempty"`
	AuthTokenEnv string `yaml:"auth_token_env,omitempty"`
}

type StrategyConfig struct {
	ID         string             `yaml:"id"`
	Exchange   string             `yaml:"exchange"`
	Instrument string             `yaml:"instrument"`
	Strategy   StrategyDefinition `yaml:"strategy"`
	Execution  StrategyExecution  `yaml:"execution"`
	TradingDay TradingDayConfig   `yaml:"trading_day"`
	Risk       StrategyRiskConfig `yaml:"risk,omitempty"`
}

type StrategyDefinition struct {
	Type   string         `yaml:"type"`
	Params StrategyParams `yaml:"params"`
}

type StrategyParams struct {
	CandleInterval string `yaml:"candle_interval"`
	FastPeriod     int    `yaml:"fast_period"`
	SlowPeriod     int    `yaml:"slow_period"`
	DayOfMonth     int    `yaml:"day_of_month"`
	Time           string `yaml:"time"`
	Timezone       string `yaml:"timezone"`
}

// MovingAverageCrossParams is kept as an alias for callers constructing a
// version-1 configuration programmatically.
type MovingAverageCrossParams = StrategyParams

type StrategyExecution struct {
	Quantity  string `yaml:"quantity"`
	OrderType string `yaml:"order_type"`
}

type TradingDayConfig struct {
	Timezone string `yaml:"timezone"`
	ResetAt  string `yaml:"reset_at"`
}

type StrategyRiskConfig struct {
	MaxDailyLoss     *DailyLossLimit `yaml:"max_daily_loss,omitempty"`
	MaxPositionValue *MoneyConfig    `yaml:"max_position_value,omitempty"`
}

type MoneyConfig struct {
	Amount string `yaml:"amount"`
	Asset  string `yaml:"asset"`
}

type DailyLossLimit struct {
	Amount string `yaml:"amount"`
	Asset  string `yaml:"asset"`
	PnL    string `yaml:"pnl"`
	Action string `yaml:"action"`
}

type TerminalConfig struct {
	RefreshInterval string        `yaml:"refresh_interval,omitempty"`
	Tabs            []TerminalTab `yaml:"tabs,omitempty"`
}

type TerminalTab struct {
	Title      string          `yaml:"title"`
	Exchange   string          `yaml:"exchange"`
	Instrument string          `yaml:"instrument"`
	Panels     []TerminalPanel `yaml:"panels,omitempty"`
}

type TerminalPanel struct {
	Type     string         `yaml:"type"`
	Mode     string         `yaml:"mode,omitempty"`
	Interval string         `yaml:"interval,omitempty"`
	History  int            `yaml:"history,omitempty"`
	Depth    int            `yaml:"depth,omitempty"`
	Limit    int            `yaml:"limit,omitempty"`
	Overlays []ChartOverlay `yaml:"overlays,omitempty"`
}

type ChartOverlay struct {
	Type   string `yaml:"type"`
	Period int    `yaml:"period"`
}

type BacktestConfig struct {
	Runs []BacktestRun `yaml:"runs,omitempty"`
}

type BacktestRun struct {
	ID        string            `yaml:"id"`
	Strategy  string            `yaml:"strategy"`
	Data      BacktestData      `yaml:"data"`
	Execution BacktestExecution `yaml:"execution"`
	Output    BacktestOutput    `yaml:"output"`
}

type BacktestData struct {
	Type         string `yaml:"type"`
	Path         string `yaml:"path"`
	MetadataPath string `yaml:"metadata_path,omitempty"`
	Interval     string `yaml:"interval"`
	PriceAsset   string `yaml:"price_asset,omitempty"`
	Timezone     string `yaml:"timezone"`
	TickSize     string `yaml:"tick_size,omitempty"`
	LotSize      string `yaml:"lot_size,omitempty"`
	GapPolicy    string `yaml:"gap_policy"`
}

type BacktestExecution struct {
	InitialCash MoneyConfig       `yaml:"initial_cash"`
	Commission  BacktestRateModel `yaml:"commission"`
	Slippage    BacktestRateModel `yaml:"slippage"`
	MarketFill  string            `yaml:"market_fill"`
	LimitFill   string            `yaml:"limit_fill"`
}

type BacktestRateModel struct {
	Type  string `yaml:"type"`
	Value string `yaml:"value"`
}

type BacktestOutput struct {
	Directory string `yaml:"directory"`
	JSON      bool   `yaml:"json"`
	TradesCSV bool   `yaml:"trades_csv"`
}
