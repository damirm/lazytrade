package agent

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"time"

	"github.com/damirm/lazytrade/internal/domain"
	"github.com/damirm/lazytrade/internal/exchange"
	"github.com/damirm/lazytrade/internal/storage"
)

func (r Runtime) submitIntent(ctx context.Context, intent storage.OrderIntent, request exchange.NewOrder) error {
	if intent.Status != "ready" {
		return fmt.Errorf("intent %s is %s, expected ready", intent.ID, intent.Status)
	}
	now := r.intentEventTime(intent)
	if err := r.Intents.TransitionOrderIntent(ctx, storage.IntentTransition{
		IntentID: intent.ID, FromStatus: "ready", ToStatus: "submitting",
		Audit: intentTransitionAudit(intent, "ready", "submitting", "submission_started", "", now),
	}); err != nil {
		return fmt.Errorf("begin order intent submission %s: %w", intent.ID, err)
	}
	intent.Status = "submitting"
	intent.UpdatedAt = now
	r.logger().InfoContext(ctx, "submitting order", "event", "order_submitting", "intent_id", intent.ID, "strategy_id", intent.StrategyID, "instrument_id", intent.InstrumentID, "side", intent.Side, "order_type", intent.OrderType, "quantity", intent.Quantity.Value.String())
	order, err := r.Exchange.PlaceOrder(ctx, request)
	if err == nil {
		if persistErr := r.recordSubmitted(ctx, intent, order, "placed"); persistErr != nil {
			return blockRuntime(persistErr)
		}
		return nil
	}
	var exchangeErr *exchange.Error
	if errors.As(err, &exchangeErr) && exchangeErr.Outcome == exchange.OutcomeUnknown {
		r.logger().ErrorContext(ctx, "order outcome unknown", "event", "order_outcome_unknown", "intent_id", intent.ID, "error", err)
		if persistErr := r.recordIntentWithoutOrder(ctx, intent, "unknown", "order_outcome_unknown", err.Error()); persistErr != nil {
			return errors.Join(err, persistErr)
		}
		return fmt.Errorf("place persisted intent %s has unknown outcome: %w", intent.ID, err)
	}
	if exchange.IsCategory(err, exchange.ErrorRejected) ||
		exchange.IsCategory(err, exchange.ErrorInvalidRequest) ||
		exchange.IsCategory(err, exchange.ErrorInsufficientFunds) ||
		exchange.IsCategory(err, exchange.ErrorPermission) ||
		exchange.IsCategory(err, exchange.ErrorAuthentication) {
		r.logger().WarnContext(ctx, "order rejected", "event", "order_rejected", "intent_id", intent.ID, "error", err)
		if persistErr := r.recordIntentWithoutOrder(ctx, intent, "rejected", "order_rejected", err.Error()); persistErr != nil {
			return errors.Join(err, persistErr)
		}
		return nil
	}
	if errors.As(err, &exchangeErr) && exchangeErr.Outcome == exchange.OutcomeKnownNotApplied &&
		(exchangeErr.Category == exchange.ErrorRateLimited || exchangeErr.Category == exchange.ErrorTransient) {
		now = r.intentEventTime(intent)
		transitionErr := r.Intents.TransitionOrderIntent(ctx, storage.IntentTransition{
			IntentID: intent.ID, FromStatus: "submitting", ToStatus: "ready",
			Audit: intentTransitionAudit(intent, "submitting", "ready", "submission_not_applied", err.Error(), now),
		})
		if transitionErr != nil {
			return errors.Join(err, transitionErr)
		}
		return fmt.Errorf("place persisted order intent %s was not applied: %w", intent.ID, err)
	}
	if !errors.As(err, &exchangeErr) {
		if persistErr := r.recordIntentWithoutOrder(ctx, intent, "unknown", "order_outcome_unclassified", err.Error()); persistErr != nil {
			return errors.Join(err, persistErr)
		}
		return fmt.Errorf("place persisted intent %s has unclassified outcome: %w", intent.ID, err)
	}
	return fmt.Errorf("place persisted order intent %s: %w", intent.ID, err)
}

func (r Runtime) recordSubmitted(ctx context.Context, intent storage.OrderIntent, order domain.Order, source string) error {
	if err := validateOrderForIntent(intent, order); err != nil {
		return fmt.Errorf("recovered order does not match intent: %w", err)
	}
	now := r.intentEventTime(intent)
	status := orderStatus(order.Status)
	recordID := sha256.Sum256([]byte("exchange-order/v1:" + intent.ID + ":" + string(order.ID)))
	record := storage.ExchangeOrder{
		ID: hex.EncodeToString(recordID[:]), OrderIntentID: intent.ID,
		ExchangeAccountID: order.ExchangeAccountID, ExchangeOrderID: order.ID,
		Status: status, RequestedQuantity: order.Quantity, FilledQuantity: order.FilledQuantity,
		SubmittedAt: order.SubmittedAt.UTC(), UpdatedAt: order.UpdatedAt.UTC(),
	}
	// The durable transition is a local observation. Exchange timestamps belong
	// to the order record and may be older or use a different clock.
	audit := resolutionAudit(intent, "submitted", source, now)
	if err := r.Intents.ResolveOrderIntent(ctx, storage.IntentResolution{
		IntentID: intent.ID, Status: "submitted", Order: &record, Audit: audit,
	}); err != nil {
		return fmt.Errorf("persist exchange order: %w", err)
	}
	if r.OnOrder != nil {
		r.OnOrder(order)
	}
	r.logger().InfoContext(ctx, "order submitted", "event", "order_submitted", "intent_id", intent.ID, "order_id", order.ID, "strategy_id", intent.StrategyID, "instrument_id", intent.InstrumentID, "status", order.Status, "source", source)
	return nil
}

func (r Runtime) recordIntentWithoutOrder(ctx context.Context, intent storage.OrderIntent, status, code, reason string) error {
	now := r.intentEventTime(intent)
	return r.Intents.ResolveOrderIntent(ctx, storage.IntentResolution{
		IntentID: intent.ID, Status: status,
		Audit: resolutionAudit(intent, status, code+":"+reason, now),
	})
}

func (r Runtime) intentEventTime(intent storage.OrderIntent) time.Time {
	now := time.Now().UTC()
	if r.Now != nil {
		now = r.Now().UTC()
	}
	// SQLite stores microseconds and ListAudit uses (created_at, id), so keep
	// consecutive transitions strictly ordered even with a fixed/coarse clock.
	if !now.After(intent.UpdatedAt) {
		return intent.UpdatedAt.Add(time.Microsecond)
	}
	return now
}

func validateOrderForIntent(intent storage.OrderIntent, order domain.Order) error {
	if order.ClientOrderID != intent.ClientOrderID ||
		order.ExchangeAccountID != intent.ExchangeAccountID ||
		order.InstrumentID != intent.InstrumentID || order.Side != intent.Side ||
		order.Type != intent.OrderType || !order.Quantity.Value.Equal(intent.Quantity.Value) {
		return errors.New("client ID, account, instrument, side, type, and quantity must match")
	}
	if (intent.LimitPrice == nil) != (order.LimitPrice == nil) {
		return errors.New("limit price presence does not match")
	}
	if intent.LimitPrice != nil &&
		(!intent.LimitPrice.Value.Equal(order.LimitPrice.Value) || intent.LimitPrice.Asset != order.LimitPrice.Asset) {
		return errors.New("limit price does not match")
	}
	return nil
}

func resolutionAudit(intent storage.OrderIntent, status, reason string, at time.Time) storage.AuditEvent {
	payload, _ := json.Marshal(struct {
		IntentID string `json:"intent_id"`
		Status   string `json:"status"`
		Reason   string `json:"reason"`
	}{intent.ID, status, reason})
	sum := sha256.Sum256([]byte("audit/order-resolution/v1:" + intent.ID + ":" + status))
	return storage.AuditEvent{
		ID: hex.EncodeToString(sum[:]), EventType: "order_intent_" + status,
		Actor: "agent", ScopeType: "strategy", ScopeID: string(intent.StrategyID),
		Payload: payload, CreatedAt: at,
	}
}

func intentTransitionAudit(
	intent storage.OrderIntent,
	from string,
	to string,
	code string,
	reason string,
	at time.Time,
) storage.AuditEvent {
	payload, _ := json.Marshal(struct {
		IntentID string `json:"intent_id"`
		From     string `json:"from"`
		To       string `json:"to"`
		Code     string `json:"code"`
		Reason   string `json:"reason,omitempty"`
	}{intent.ID, from, to, code, reason})
	sum := sha256.Sum256([]byte(fmt.Sprintf(
		"audit/order-transition/v1:%s:%s:%s:%d", intent.ID, from, to, at.UnixNano(),
	)))
	return storage.AuditEvent{
		ID: hex.EncodeToString(sum[:]), EventType: "order_intent_" + to,
		Actor: "agent", ScopeType: "strategy", ScopeID: string(intent.StrategyID),
		Payload: payload, CreatedAt: at,
	}
}

func requestForIntent(intent storage.OrderIntent) exchange.NewOrder {
	return exchange.NewOrder{
		ClientOrderID: intent.ClientOrderID, StrategyID: intent.StrategyID,
		ExchangeAccountID: intent.ExchangeAccountID, InstrumentID: intent.InstrumentID,
		Side: intent.Side, Type: intent.OrderType, Quantity: intent.Quantity,
		LimitPrice: intent.LimitPrice,
	}
}

func orderStatus(status domain.OrderStatus) string {
	switch status {
	case domain.OrderStatusPending:
		return "pending"
	case domain.OrderStatusAccepted:
		return "accepted"
	case domain.OrderStatusPartiallyFilled:
		return "partially_filled"
	case domain.OrderStatusFilled:
		return "filled"
	case domain.OrderStatusCancelled:
		return "cancelled"
	case domain.OrderStatusRejected:
		return "rejected"
	default:
		return "unknown"
	}
}
