package config_test

import (
	"path/filepath"
	"strings"
	"testing"

	"github.com/damirm/lazytrade/internal/config"
)

func TestLoadValidConfig(t *testing.T) {
	t.Parallel()

	cfg, err := config.Load([]byte(`
version: 1
database:
  driver: sqlite
  dsn: ./lazytrade.db
logging:
  level: info
  format: json
exchanges: {}
agent: {}
terminal: {}
backtest: {}
`))
	if err != nil {
		t.Fatalf("Load() error = %v", err)
	}
	if cfg.Version != config.CurrentVersion {
		t.Fatalf("Version = %d, want %d", cfg.Version, config.CurrentVersion)
	}
	if cfg.Database.Driver != "sqlite" {
		t.Fatalf("Database.Driver = %q, want sqlite", cfg.Database.Driver)
	}
}

func TestLoadExample(t *testing.T) {
	t.Parallel()

	_, err := config.LoadFile(filepath.Join("..", "..", "configs", "example.yaml"))
	if err != nil {
		t.Fatalf("LoadFile(example) error = %v", err)
	}
}

func TestValidatePeriodicInvestment(t *testing.T) {
	t.Parallel()
	cfg := validConfig()
	strategy := &cfg.Agent.Strategies[0]
	strategy.Strategy = config.StrategyDefinition{
		Type: "periodic_investment",
		Params: config.StrategyParams{
			CandleInterval: "1m", DayOfMonth: 10, Time: "11:00", Timezone: "Europe/Moscow",
		},
	}
	if err := cfg.Validate(); err != nil {
		t.Fatal(err)
	}
}

func TestValidatePeriodicInvestmentRejectsUnsafeSchedule(t *testing.T) {
	t.Parallel()
	cfg := validConfig()
	strategy := &cfg.Agent.Strategies[0]
	strategy.Strategy = config.StrategyDefinition{
		Type: "periodic_investment",
		Params: config.StrategyParams{
			CandleInterval: "1m", DayOfMonth: 31, Time: "11:00", Timezone: "Europe/Moscow",
		},
	}
	err := cfg.Validate()
	if err == nil || !strings.Contains(err.Error(), "day_of_month") {
		t.Fatalf("Validate() error = %v, want day_of_month path", err)
	}
}

func TestLoadRejectsUnknownField(t *testing.T) {
	t.Parallel()

	_, err := config.Load([]byte(`
version: 1
unknown_section: {}
`))
	if err == nil {
		t.Fatal("Load() error = nil, want unknown field error")
	}
	if !strings.Contains(err.Error(), "field unknown_section not found") {
		t.Fatalf("Load() error = %q, want unknown field path", err)
	}
}

func TestLoadRejectsNestedUnknownField(t *testing.T) {
	t.Parallel()

	_, err := config.Load([]byte(`
version: 1
agent:
  strategies:
    - id: test
      exchange: missing
      instrument: TEST
      strategy:
        type: moving_average_cross
        params:
          candle_interval: 1m
          fast_period: 2
          slow_period: 3
          surprise: true
`))
	if err == nil {
		t.Fatal("Load() error = nil, want nested unknown field error")
	}
	if !strings.Contains(err.Error(), "field surprise not found") {
		t.Fatalf("Load() error = %q, want nested field name", err)
	}
}

func TestValidateRejectsUnsupportedLoggingSettings(t *testing.T) {
	t.Parallel()

	for _, change := range []func(*config.Config){
		func(cfg *config.Config) { cfg.Logging.Level = "trace" },
		func(cfg *config.Config) { cfg.Logging.Format = "xml" },
	} {
		cfg := validConfig()
		change(&cfg)
		if err := cfg.Validate(); err == nil || !strings.Contains(err.Error(), "config.logging") {
			t.Fatalf("Validate() error = %v, want logging field error", err)
		}
	}
}

func TestValidateRejectsDuplicateStrategyIDs(t *testing.T) {
	t.Parallel()

	cfg := validConfig()
	cfg.Agent.Strategies = append(cfg.Agent.Strategies, cfg.Agent.Strategies[0])
	err := cfg.Validate()
	if err == nil || !strings.Contains(err.Error(), `duplicate strategy ID "strategy-1"`) {
		t.Fatalf("Validate() error = %v, want duplicate ID", err)
	}
}

func TestValidateRejectsMissingExchangeReference(t *testing.T) {
	t.Parallel()

	cfg := validConfig()
	cfg.Agent.Strategies[0].Exchange = "missing"
	err := cfg.Validate()
	if err == nil || !strings.Contains(err.Error(), `config.agent.strategies[0].exchange`) {
		t.Fatalf("Validate() error = %v, want exchange field path", err)
	}
}

func TestValidateRejectsInvalidDecimal(t *testing.T) {
	t.Parallel()

	tests := []string{"1e3", "1,5", "NaN", ".5", "1."}
	for _, value := range tests {
		value := value
		t.Run(value, func(t *testing.T) {
			t.Parallel()
			cfg := validConfig()
			cfg.Agent.Strategies[0].Execution.Quantity = value
			err := cfg.Validate()
			if err == nil || !strings.Contains(err.Error(), `config.agent.strategies[0].execution.quantity`) {
				t.Fatalf("Validate() error = %v, want decimal field path", err)
			}
		})
	}
}

func TestValidateForBacktestDoesNotRequireCredentials(t *testing.T) {
	t.Parallel()

	cfg := validConfig()
	cfg.Exchanges["main"] = config.ExchangeConfig{Type: "tinvest"}
	cfg.Agent.Strategies[0].Exchange = "main"
	cfg.Backtest.Runs = []config.BacktestRun{{
		ID:       "offline",
		Strategy: "strategy-1",
		Data: config.BacktestData{
			Type:       "csv",
			Path:       "test.csv",
			Interval:   "1m",
			PriceAsset: "USD",
			Timezone:   "UTC",
			TickSize:   "0.01",
			LotSize:    "1",
			GapPolicy:  "fail",
		},
		Execution: config.BacktestExecution{
			InitialCash: config.MoneyConfig{Amount: "1000", Asset: "USD"},
			Commission:  config.BacktestRateModel{Type: "percent", Value: "0"},
			Slippage:    config.BacktestRateModel{Type: "basis_points", Value: "0"},
			MarketFill:  "next_open",
			LimitFill:   "touch",
		},
		Output: config.BacktestOutput{Directory: "out", JSON: true},
	}}
	called := false
	err := cfg.ValidateFor(config.CommandBacktest, func(string) (string, bool) {
		called = true
		return "", false
	})
	if err != nil {
		t.Fatalf("ValidateFor(backtest) error = %v", err)
	}
	if called {
		t.Fatal("ValidateFor(backtest) resolved credentials")
	}
}

func TestValidateForAgentRequiresCredentials(t *testing.T) {
	t.Parallel()

	cfg := validConfig()
	err := cfg.ValidateFor(config.CommandAgent, func(name string) (string, bool) {
		if name == "TOKEN" {
			return "secret", true
		}
		return "", false
	})
	if err == nil || !strings.Contains(err.Error(), `"ACCOUNT" is not set`) {
		t.Fatalf("ValidateFor(agent) error = %v, want missing account env", err)
	}
}

func validConfig() config.Config {
	return config.Config{
		Version: config.CurrentVersion,
		Exchanges: map[string]config.ExchangeConfig{
			"main": {
				Type:         "tinvest",
				TokenEnv:     "TOKEN",
				AccountIDEnv: "ACCOUNT",
				Sandbox:      true,
			},
		},
		Agent: config.AgentConfig{
			Strategies: []config.StrategyConfig{{
				ID:         "strategy-1",
				Exchange:   "main",
				Instrument: "TEST",
				Strategy: config.StrategyDefinition{
					Type: "moving_average_cross",
					Params: config.MovingAverageCrossParams{
						CandleInterval: "1m",
						FastPeriod:     2,
						SlowPeriod:     3,
					},
				},
				Execution:  config.StrategyExecution{Quantity: "1", OrderType: "market"},
				TradingDay: config.TradingDayConfig{Timezone: "UTC", ResetAt: "00:00"},
			}},
		},
	}
}
