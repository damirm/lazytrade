package risk

import "errors"

type StrategyStatus string

const (
	StatusRunning    StrategyStatus = "running"
	StatusRiskPaused StrategyStatus = "risk_paused"
)

var (
	ErrInvalidStatus      = errors.New("invalid strategy status")
	ErrResumeConfirmation = errors.New("resuming a risk-paused strategy requires explicit confirmation")
)

// Pause is idempotent and never weakens an existing safe state.
func Pause(status StrategyStatus) (StrategyStatus, error) {
	switch status {
	case StatusRunning, StatusRiskPaused:
		return StatusRiskPaused, nil
	default:
		return "", ErrInvalidStatus
	}
}

// Resume is the only transition from risk_paused to running. A trading-day
// rollover deliberately does not call it.
func Resume(status StrategyStatus, explicitlyConfirmed bool) (StrategyStatus, error) {
	switch status {
	case StatusRunning:
		return StatusRunning, nil
	case StatusRiskPaused:
		if !explicitlyConfirmed {
			return StatusRiskPaused, ErrResumeConfirmation
		}
		return StatusRunning, nil
	default:
		return "", ErrInvalidStatus
	}
}
