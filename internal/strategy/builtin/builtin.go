package builtin

import (
	"fmt"
	"time"

	"github.com/damirm/lazytrade/internal/config"
	"github.com/damirm/lazytrade/internal/domain"
	"github.com/damirm/lazytrade/internal/strategy"
	"github.com/damirm/lazytrade/internal/strategy/movingaverage"
	"github.com/damirm/lazytrade/internal/strategy/periodicinvestment"
)

// Build is the single composition point for built-in strategy types used by
// live runtime and backtests.
func Build(configured config.StrategyConfig) (strategy.Strategy, error) {
	interval, err := time.ParseDuration(configured.Strategy.Params.CandleInterval)
	if err != nil {
		return nil, fmt.Errorf("parse candle interval: %w", err)
	}
	quantity, err := domain.NewQuantity(configured.Execution.Quantity)
	if err != nil {
		return nil, fmt.Errorf("parse execution quantity: %w", err)
	}
	params := configured.Strategy.Params
	switch configured.Strategy.Type {
	case movingaverage.Type:
		return movingaverage.New(movingaverage.Config{
			FastPeriod: params.FastPeriod, SlowPeriod: params.SlowPeriod,
			Interval: interval, Quantity: quantity,
		})
	case periodicinvestment.Type:
		location, loadErr := time.LoadLocation(params.Timezone)
		if loadErr != nil {
			return nil, fmt.Errorf("load schedule timezone: %w", loadErr)
		}
		return periodicinvestment.New(periodicinvestment.Config{
			Interval: interval, Quantity: quantity, DayOfMonth: params.DayOfMonth,
			TimeOfDay: params.Time, Location: location,
		})
	default:
		return nil, fmt.Errorf("unsupported strategy type %q", configured.Strategy.Type)
	}
}

func CandleInterval(configured config.StrategyConfig) (time.Duration, error) {
	interval, err := time.ParseDuration(configured.Strategy.Params.CandleInterval)
	if err != nil {
		return 0, fmt.Errorf("parse candle interval: %w", err)
	}
	return interval, nil
}
