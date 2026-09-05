package backtest

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io"

	"github.com/damirm/lazytrade/internal/clock"
	"github.com/damirm/lazytrade/internal/domain"
)

type EventProcessor interface {
	Process(context.Context, domain.MarketEvent) ([]domain.Signal, error)
}

type RiskDecision struct {
	Allowed    bool
	Paused     bool
	ReasonCode string
}

// RiskEvaluator is deliberately narrower than the future risk manager. The
// adapter owns conversion from a shared risk decision to this value; backtest
// does not contain or duplicate any limit logic.
type RiskEvaluator interface {
	Evaluate(context.Context, domain.Signal, PortfolioSnapshot) (RiskDecision, error)
}

// RiskObserver is implemented by stateful risk evaluators. The runner calls it
// after the broker has marked the portfolio and applied fills, and before any
// signal produced by the same event is evaluated.
type RiskObserver interface {
	Observe(context.Context, domain.MarketEvent, []domain.Execution, PortfolioSnapshot) (RiskDecision, error)
}

type AllowAllRisk struct{}

func (AllowAllRisk) Evaluate(context.Context, domain.Signal, PortfolioSnapshot) (RiskDecision, error) {
	return RiskDecision{Allowed: true}, nil
}

type Runner struct {
	Iterator EventIterator
	Clock    clock.MutableClock
	Strategy EventProcessor
	Risk     RiskEvaluator
	Broker   *SimulatedBroker
}

func (r Runner) Run(ctx context.Context) (Report, error) {
	if r.Iterator == nil || r.Clock == nil || r.Strategy == nil || r.Risk == nil || r.Broker == nil {
		return Report{}, errors.New("iterator, clock, strategy, risk, and broker are required")
	}
	report := NewReport(r.Broker.config)
	for {
		event, err := r.Iterator.Next(ctx)
		if errors.Is(err, io.EOF) {
			break
		}
		if err != nil {
			return Report{}, err
		}
		if err := r.Clock.AdvanceTo(event.ExchangeTime); err != nil {
			return Report{}, fmt.Errorf("advance virtual clock: %w", err)
		}
		executions, err := r.Broker.OnMarketEvent(ctx, event)
		if err != nil {
			return Report{}, fmt.Errorf("broker event: %w", err)
		}
		report.Executions = append(report.Executions, executions...)
		report.Observe(event, executions, r.Broker.Snapshot())
		if observer, ok := r.Risk.(RiskObserver); ok {
			decision, err := observer.Observe(ctx, event, executions, r.Broker.Snapshot())
			if err != nil {
				return Report{}, fmt.Errorf("risk observation: %w", err)
			}
			if !decision.Allowed {
				if decision.Paused {
					report.Metrics.RiskPauses++
				}
				report.LastRiskReason = decision.ReasonCode
			}
		}
		signals, err := r.Strategy.Process(ctx, event)
		if err != nil {
			return Report{}, fmt.Errorf("strategy event: %w", err)
		}
		for _, signal := range signals {
			decision, err := r.Risk.Evaluate(ctx, signal, r.Broker.Snapshot())
			if err != nil {
				return Report{}, fmt.Errorf("risk evaluation: %w", err)
			}
			if !decision.Allowed {
				report.Metrics.RiskRejections++
				report.LastRiskReason = decision.ReasonCode
				continue
			}
			request, err := requestForSignal(signal)
			if err != nil {
				return Report{}, err
			}
			_, err = r.Broker.Submit(ctx, request)
			if err != nil {
				return Report{}, fmt.Errorf("submit simulated order: %w", err)
			}
		}
		report.ObserveEquity(r.Broker.Snapshot().Equity)
	}
	report.Orders = r.Broker.Orders()
	report.Finalize(r.Broker.Snapshot())
	return report, nil
}

func requestForSignal(signal domain.Signal) (SubmitRequest, error) {
	side := domain.OrderSideBuy
	switch signal.Action {
	case domain.SignalBuy:
	case domain.SignalSell, domain.SignalClose:
		side = domain.OrderSideSell
	default:
		return SubmitRequest{}, errors.New("unsupported signal action")
	}
	sum := sha256.Sum256([]byte("order/v1:" + string(signal.ID)))
	id := hex.EncodeToString(sum[:])
	return SubmitRequest{
		OrderID: domain.OrderID(id), ClientOrderID: domain.ClientOrderID(id),
		StrategyID: signal.StrategyID, InstrumentID: signal.InstrumentID,
		Side: side, Type: signal.OrderType, Quantity: signal.Quantity,
		LimitPrice: signal.LimitPrice, SubmittedAt: signal.CreatedAt.UTC(),
		CausativeCursor: signal.CausativeCursor,
	}, nil
}
