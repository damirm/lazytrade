package composition

import (
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/damirm/lazytrade/internal/config"
	"github.com/damirm/lazytrade/internal/domain"
	"github.com/damirm/lazytrade/internal/risk"
)

func TestBuildStrategyRiskLiveBacktestParity(t *testing.T) {
	strategy := config.StrategyConfig{
		ID: "monthly", TradingDay: config.TradingDayConfig{Timezone: "Europe/Moscow", ResetAt: "07:30"},
		Risk: config.StrategyRiskConfig{
			MaxPositionValue: &config.MoneyConfig{Amount: "1234.56", Asset: "RUB"},
			MaxDailyLoss:     &config.DailyLossLimit{Amount: "78.90", Asset: "RUB", PnL: string(risk.PnLTotal), Action: "pause"},
		},
	}
	backtestAsset, err := domain.NormalizeAsset("rub")
	if err != nil {
		t.Fatal(err)
	}
	live, err := BuildStrategyRisk(strategy, "RUB")
	if err != nil {
		t.Fatal(err)
	}
	backtest, err := BuildStrategyRisk(strategy, backtestAsset)
	if err != nil {
		t.Fatal(err)
	}
	for _, got := range []risk.Config{live, backtest} {
		if got.StrategyID != "monthly" || got.SettlementAsset != "RUB" || got.MaxPositionValue == nil || got.MaxDailyLoss == nil {
			t.Fatalf("incomplete risk config: %+v", got)
		}
		if got.MaxPositionValue.Amount.String() != "1234.56" || got.MaxPositionValue.Asset != "RUB" || got.MaxDailyLoss.Limit.Amount.String() != "78.9" || got.MaxDailyLoss.Limit.Asset != "RUB" || got.MaxDailyLoss.Mode != risk.PnLTotal {
			t.Fatalf("unexpected risk limits: %+v", got)
		}
	}
	reset := time.Date(2026, 9, 22, 4, 30, 0, 0, time.UTC)
	for _, at := range []time.Time{reset.Add(-time.Nanosecond), reset, reset.Add(time.Nanosecond)} {
		liveDay, backtestDay := live.TradingDay.At(at), backtest.TradingDay.At(at)
		if liveDay.Key != backtestDay.Key || !liveDay.StartsAt.Equal(backtestDay.StartsAt) || !liveDay.EndsAt.Equal(backtestDay.EndsAt) {
			t.Fatalf("trading-day mismatch at %s: %+v vs %+v", at, liveDay, backtestDay)
		}
		wantKey, wantStart, wantEnd := "2026-09-22", reset, reset.AddDate(0, 0, 1)
		if at.Before(reset) {
			wantKey, wantStart, wantEnd = "2026-09-21", reset.AddDate(0, 0, -1), reset
		}
		if liveDay.Key != wantKey || !liveDay.StartsAt.Equal(wantStart) || !liveDay.EndsAt.Equal(wantEnd) {
			t.Fatalf("unexpected day at %s: %+v", at, liveDay)
		}
	}
}

func TestBuildStrategyRiskErrors(t *testing.T) {
	base := config.StrategyConfig{ID: "s", TradingDay: config.TradingDayConfig{Timezone: "UTC", ResetAt: "00:00"}}
	badTimezone := base
	badTimezone.TradingDay.Timezone = "bad/timezone"
	badPosition := base
	badPosition.Risk.MaxPositionValue = &config.MoneyConfig{Amount: "not-decimal", Asset: "RUB"}
	badDaily := base
	badDaily.Risk.MaxDailyLoss = &config.DailyLossLimit{Amount: "not-decimal", Asset: "RUB", PnL: "total"}
	for _, tc := range []struct {
		name     string
		strategy config.StrategyConfig
		policy   bool
	}{
		{"timezone", badTimezone, true}, {"position", badPosition, false}, {"daily", badDaily, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			got, err := BuildStrategyRisk(tc.strategy, "RUB")
			if err == nil || got != (risk.Config{}) {
				t.Fatalf("got %+v, %v", got, err)
			}
			var policyErr *TradingDayPolicyError
			if errors.As(err, &policyErr) != tc.policy {
				t.Fatalf("wrong error phase: %v", err)
			}
			if !tc.policy && !strings.Contains(err.Error(), "decimal") {
				t.Fatalf("missing decimal cause: %v", err)
			}
		})
	}
}
