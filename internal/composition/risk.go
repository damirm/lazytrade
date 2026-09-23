package composition

import (
	"github.com/damirm/lazytrade/internal/config"
	"github.com/damirm/lazytrade/internal/domain"
	"github.com/damirm/lazytrade/internal/risk"
)

// TradingDayPolicyError distinguishes policy construction from limit conversion.
type TradingDayPolicyError struct{ Err error }

func (e *TradingDayPolicyError) Error() string { return e.Err.Error() }
func (e *TradingDayPolicyError) Unwrap() error { return e.Err }

// BuildStrategyRisk maps a configured strategy and resolved settlement asset to
// risk settings. It leaves action policy, asset normalization and evaluator
// construction to the caller.
func BuildStrategyRisk(strategy config.StrategyConfig, settlementAsset string) (risk.Config, error) {
	policy, err := risk.NewTradingDayPolicy(strategy.TradingDay.Timezone, strategy.TradingDay.ResetAt)
	if err != nil {
		return risk.Config{}, &TradingDayPolicyError{Err: err}
	}
	result := risk.Config{StrategyID: domain.StrategyID(strategy.ID), SettlementAsset: settlementAsset, TradingDay: policy}
	if configured := strategy.Risk.MaxPositionValue; configured != nil {
		value, err := domain.NewMoney(configured.Amount, configured.Asset)
		if err != nil {
			return risk.Config{}, err
		}
		result.MaxPositionValue = &value
	}
	if configured := strategy.Risk.MaxDailyLoss; configured != nil {
		value, err := domain.NewMoney(configured.Amount, configured.Asset)
		if err != nil {
			return risk.Config{}, err
		}
		result.MaxDailyLoss = &risk.DailyLossLimit{Limit: value, Mode: risk.PnLMode(configured.PnL)}
	}
	return result, nil
}
