package builtin_test

import (
	"testing"
	"time"

	"github.com/damirm/lazytrade/internal/config"
	"github.com/damirm/lazytrade/internal/strategy/builtin"
	"github.com/damirm/lazytrade/internal/strategy/movingaverage"
	"github.com/damirm/lazytrade/internal/strategy/periodicinvestment"
)

func TestBuildSupportedStrategies(t *testing.T) {
	t.Parallel()
	for _, test := range []struct {
		name       string
		definition config.StrategyDefinition
		wantType   string
	}{
		{
			name: "moving average",
			definition: config.StrategyDefinition{Type: movingaverage.Type, Params: config.StrategyParams{
				CandleInterval: "1m", FastPeriod: 2, SlowPeriod: 3,
			}},
			wantType: movingaverage.Type,
		},
		{
			name: "periodic investment",
			definition: config.StrategyDefinition{Type: periodicinvestment.Type, Params: config.StrategyParams{
				CandleInterval: "1h", DayOfMonth: 10, Time: "11:00", Timezone: "Europe/Moscow",
			}},
			wantType: periodicinvestment.Type,
		},
	} {
		test := test
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()
			implementation, err := builtin.Build(config.StrategyConfig{
				Strategy:  test.definition,
				Execution: config.StrategyExecution{Quantity: "1", OrderType: "market"},
			})
			if err != nil {
				t.Fatal(err)
			}
			if implementation.Type() != test.wantType {
				t.Fatalf("type = %q, want %q", implementation.Type(), test.wantType)
			}
			if requirements := implementation.RequiredData(); len(requirements.CandleIntervals) != 1 || requirements.CandleIntervals[0] <= 0 {
				t.Fatalf("requirements = %#v", requirements)
			}
		})
	}
}

func TestCandleInterval(t *testing.T) {
	t.Parallel()
	configured := config.StrategyConfig{Strategy: config.StrategyDefinition{Params: config.StrategyParams{CandleInterval: "5m"}}}
	interval, err := builtin.CandleInterval(configured)
	if err != nil || interval != 5*time.Minute {
		t.Fatalf("interval = %s, error = %v", interval, err)
	}
}
