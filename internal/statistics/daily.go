package statistics

import (
	"fmt"
	"time"

	"github.com/damirm/lazytrade/internal/domain"
)

// DailySnapshot is persisted at a trading-day boundary. Baseline contains
// cumulative component values, allowing restart-safe daily deltas.
type DailySnapshot struct {
	StrategyID    domain.StrategyID
	DayKey        string
	StartedAt     time.Time
	StartEquity   domain.Money
	Baseline      Components
	CurrentEquity domain.Money
}

func NewDailySnapshot(dayKey string, startedAt time.Time, equity domain.Money, baseline Components) (DailySnapshot, error) {
	if dayKey == "" {
		return DailySnapshot{}, fmt.Errorf("trading day key must not be empty")
	}
	if startedAt.IsZero() || startedAt.Location() != time.UTC {
		return DailySnapshot{}, fmt.Errorf("trading day start must be non-zero UTC")
	}
	if err := equity.Validate(); err != nil {
		return DailySnapshot{}, fmt.Errorf("equity: %w", err)
	}
	if err := baseline.Validate(); err != nil {
		return DailySnapshot{}, fmt.Errorf("baseline: %w", err)
	}
	if equity.Asset != baseline.Asset {
		return DailySnapshot{}, domain.ErrAssetMismatch
	}
	return DailySnapshot{
		StrategyID:    baseline.StrategyID,
		DayKey:        dayKey,
		StartedAt:     startedAt,
		StartEquity:   equity,
		Baseline:      baseline,
		CurrentEquity: equity,
	}, nil
}

func (s DailySnapshot) WithCurrent(equity domain.Money, cumulative Components) (DailySnapshot, Components, error) {
	if equity.Asset != s.StartEquity.Asset || cumulative.Asset != s.StartEquity.Asset {
		return DailySnapshot{}, Components{}, domain.ErrAssetMismatch
	}
	if cumulative.StrategyID != s.StrategyID {
		return DailySnapshot{}, Components{}, ErrStrategyMismatch
	}
	daily, err := cumulative.Delta(s.Baseline)
	if err != nil {
		return DailySnapshot{}, Components{}, err
	}
	next := s
	next.CurrentEquity = equity
	return next, daily, nil
}
