package agent

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"

	"github.com/damirm/lazytrade/internal/domain"
	"github.com/damirm/lazytrade/internal/exchange"
	"github.com/damirm/lazytrade/internal/storage"
	"github.com/damirm/lazytrade/internal/strategy"
)

func (r Runtime) processEventWith(
	ctx context.Context,
	event domain.MarketEvent,
	workers map[domain.InstrumentID]*strategy.Worker,
	risks map[domain.StrategyID]SignalRisk,
) error {
	worker, ok := workers[event.InstrumentID]
	if !ok {
		return fmt.Errorf("market event for unconfigured instrument %q", event.InstrumentID)
	}
	signals, err := worker.Process(ctx, event)
	if err != nil {
		return strategyRuntimeError(r.strategyIDForInstrument(event.InstrumentID), "worker", err)
	}
	for _, signal := range signals {
		if err := r.processSignalWith(ctx, signal, risks); err != nil {
			return err
		}
	}
	return nil
}

func (r Runtime) recoverSignalsWith(
	ctx context.Context,
	risks map[domain.StrategyID]SignalRisk,
) error {
	for {
		signals, err := r.Intents.ListSignalsPendingRisk(ctx, 100)
		if err != nil {
			return err
		}
		for _, signal := range signals {
			if err := r.processSignalWith(ctx, signal, risks); err != nil {
				return err
			}
		}
		if len(signals) < 100 {
			return nil
		}
	}
}

func (r Runtime) processSignalWith(
	ctx context.Context,
	signal domain.Signal,
	risks map[domain.StrategyID]SignalRisk,
) error {
	r.logger().InfoContext(ctx, "strategy signal received", "event", "signal_received", "signal_id", signal.ID, "strategy_id", signal.StrategyID, "instrument_id", signal.InstrumentID, "action", signal.Action, "reason_code", signal.ReasonCode)
	riskGate, ok := risks[signal.StrategyID]
	if !ok && len(risks) == 1 {
		for _, only := range risks {
			riskGate, ok = only, true
		}
	}
	if !ok {
		return fmt.Errorf("risk gate for strategy %q is not configured", signal.StrategyID)
	}
	decision, err := riskGate.Evaluate(ctx, signal)
	if err != nil {
		return fmt.Errorf("risk: %w", err)
	}
	storedDecision, decisionAudit, err := buildRiskDecision(signal, decision)
	if err != nil {
		return err
	}
	if !decision.Allowed {
		if err := r.Intents.RecordRiskDecision(ctx, storedDecision, decisionAudit); err != nil {
			return fmt.Errorf("persist risk decision: %w", err)
		}
		r.logger().WarnContext(ctx, "signal rejected by risk", "event", "risk_rejected", "signal_id", signal.ID, "strategy_id", signal.StrategyID, "reason_code", decision.ReasonCode)
		return nil
	}
	r.logger().InfoContext(ctx, "signal allowed by risk", "event", "risk_allowed", "signal_id", signal.ID, "strategy_id", signal.StrategyID)
	intent, intentAudit, orderRequest, err := buildIntent(signal)
	if err != nil {
		return err
	}
	if err := r.Intents.RecordAllowedDecisionIntent(ctx, storedDecision, intent, intentAudit); err != nil {
		return fmt.Errorf("persist allowed order intent: %w", err)
	}
	return r.submitIntent(ctx, intent, orderRequest)
}

func buildRiskDecision(signal domain.Signal, decision RiskDecision) (storage.RiskDecision, storage.AuditEvent, error) {
	kind := "reject"
	reasonCode := decision.ReasonCode
	if decision.Allowed {
		kind = "allow"
		if reasonCode == "" {
			reasonCode = "allowed"
		}
	} else if reasonCode == "" {
		reasonCode = "rejected"
	}
	payload, err := json.Marshal(struct {
		SignalID   domain.SignalID `json:"signal_id"`
		Decision   string          `json:"decision"`
		ReasonCode string          `json:"reason_code"`
		Reason     string          `json:"reason"`
	}{signal.ID, kind, reasonCode, decision.Reason})
	if err != nil {
		return storage.RiskDecision{}, storage.AuditEvent{}, err
	}
	idSum := sha256.Sum256([]byte("risk-decision/v1:" + string(signal.ID)))
	id := hex.EncodeToString(idSum[:])
	record := storage.RiskDecision{
		ID: id, SignalID: signal.ID, Decision: kind, ReasonCode: reasonCode,
		Payload: payload, CreatedAt: signal.CreatedAt.UTC(),
	}
	auditSum := sha256.Sum256([]byte("audit/risk/v1:" + id))
	audit := storage.AuditEvent{
		ID: hex.EncodeToString(auditSum[:]), EventType: "risk_decision",
		Actor: "agent", ScopeType: "strategy", ScopeID: string(signal.StrategyID),
		Payload: payload, CreatedAt: signal.CreatedAt.UTC(),
	}
	return record, audit, nil
}

func buildIntent(signal domain.Signal) (storage.OrderIntent, storage.AuditEvent, exchange.NewOrder, error) {
	if err := signal.Validate(); err != nil {
		return storage.OrderIntent{}, storage.AuditEvent{}, exchange.NewOrder{}, err
	}
	side := domain.OrderSideBuy
	if signal.Action == domain.SignalSell || signal.Action == domain.SignalClose {
		side = domain.OrderSideSell
	}
	idSum := sha256.Sum256([]byte("intent/v1:" + string(signal.ID)))
	intentID := hex.EncodeToString(idSum[:])
	clientID := deterministicClientOrderID(signal.ID)
	payload, err := json.Marshal(struct {
		SignalID      domain.SignalID      `json:"signal_id"`
		ClientOrderID domain.ClientOrderID `json:"client_order_id"`
		Side          domain.OrderSide     `json:"side"`
		OrderType     domain.OrderType     `json:"order_type"`
		Quantity      string               `json:"quantity"`
	}{
		SignalID: signal.ID, ClientOrderID: clientID, Side: side,
		OrderType: signal.OrderType, Quantity: signal.Quantity.Value.String(),
	})
	if err != nil {
		return storage.OrderIntent{}, storage.AuditEvent{}, exchange.NewOrder{}, err
	}
	payloadSum := sha256.Sum256(payload)
	now := signal.CreatedAt.UTC()
	intent := storage.OrderIntent{
		ID: intentID, SignalID: signal.ID, StrategyID: signal.StrategyID,
		ExchangeAccountID: signal.ExchangeAccountID, InstrumentID: signal.InstrumentID,
		ClientOrderID: clientID, Side: side, OrderType: signal.OrderType,
		Quantity: signal.Quantity, LimitPrice: signal.LimitPrice, Status: "ready",
		PayloadChecksum: hex.EncodeToString(payloadSum[:]), CreatedAt: now, UpdatedAt: now,
	}
	auditIDSum := sha256.Sum256([]byte("audit/intent/v1:" + intentID))
	audit := storage.AuditEvent{
		ID: hex.EncodeToString(auditIDSum[:]), EventType: "order_intent_created",
		Actor: "agent", ScopeType: "strategy", ScopeID: string(signal.StrategyID),
		Payload: payload, CreatedAt: now,
	}
	request := exchange.NewOrder{
		ClientOrderID: clientID, StrategyID: signal.StrategyID,
		ExchangeAccountID: signal.ExchangeAccountID, InstrumentID: signal.InstrumentID,
		Side: side, Type: signal.OrderType, Quantity: signal.Quantity, LimitPrice: signal.LimitPrice,
	}
	return intent, audit, request, nil
}

func deterministicClientOrderID(signalID domain.SignalID) domain.ClientOrderID {
	sum := sha256.Sum256([]byte("client-order/v1:" + string(signalID)))
	// UUID-compatible deterministic identifier for exchange idempotency keys.
	sum[6] = (sum[6] & 0x0f) | 0x50
	sum[8] = (sum[8] & 0x3f) | 0x80
	return domain.ClientOrderID(fmt.Sprintf("%x-%x-%x-%x-%x", sum[0:4], sum[4:6], sum[6:8], sum[8:10], sum[10:16]))
}
