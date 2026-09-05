package risk

import (
	"fmt"
	"time"

	appclock "github.com/damirm/lazytrade/internal/clock"
)

type TradingDay struct {
	Key      string
	StartsAt time.Time
	EndsAt   time.Time
}

type TradingDayPolicy struct {
	location    *time.Location
	resetHour   int
	resetMinute int
}

func (p TradingDayPolicy) Validate() error {
	if p.location == nil {
		return fmt.Errorf("trading-day policy is not initialized")
	}
	return nil
}

func NewTradingDayPolicy(timezone, resetAt string) (TradingDayPolicy, error) {
	location, err := time.LoadLocation(timezone)
	if err != nil {
		return TradingDayPolicy{}, fmt.Errorf("load trading-day timezone %q: %w", timezone, err)
	}
	parsed, err := time.Parse("15:04", resetAt)
	if err != nil {
		return TradingDayPolicy{}, fmt.Errorf("parse trading-day reset %q: %w", resetAt, err)
	}
	return TradingDayPolicy{
		location:    location,
		resetHour:   parsed.Hour(),
		resetMinute: parsed.Minute(),
	}, nil
}

func (p TradingDayPolicy) Current(clock appclock.Clock) TradingDay {
	return p.At(clock.Now())
}

func (p TradingDayPolicy) At(now time.Time) TradingDay {
	local := now.In(p.location)
	start := time.Date(local.Year(), local.Month(), local.Day(), p.resetHour, p.resetMinute, 0, 0, p.location)
	if local.Before(start) {
		previous := local.AddDate(0, 0, -1)
		start = time.Date(previous.Year(), previous.Month(), previous.Day(), p.resetHour, p.resetMinute, 0, 0, p.location)
	}
	next := start.AddDate(0, 0, 1)
	return TradingDay{
		Key:      start.Format("2006-01-02"),
		StartsAt: start.UTC(),
		EndsAt:   next.UTC(),
	}
}
