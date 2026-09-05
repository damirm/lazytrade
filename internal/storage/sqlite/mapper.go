package sqlite

import (
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"time"

	"github.com/damirm/lazytrade/internal/domain"
	"github.com/damirm/lazytrade/internal/storage"
	"github.com/damirm/lazytrade/internal/storage/sqlite/generated"
)

func micros(value time.Time) (int64, error) {
	if value.IsZero() || value.Location() != time.UTC {
		return 0, errors.New("timestamp must be non-zero UTC")
	}
	return value.UnixMicro(), nil
}

func fromMicros(value int64) time.Time { return time.UnixMicro(value).UTC() }

func signalChecksum(signal domain.Signal) (string, error) {
	payload, err := json.Marshal(signal)
	if err != nil {
		return "", fmt.Errorf("encode signal checksum: %w", err)
	}
	sum := sha256.Sum256(payload)
	return hex.EncodeToString(sum[:]), nil
}

func intentParams(intent storage.OrderIntent) (generated.InsertOrderIntentParams, error) {
	created, err := micros(intent.CreatedAt)
	if err != nil {
		return generated.InsertOrderIntentParams{}, fmt.Errorf("intent created at: %w", err)
	}
	updated, err := micros(intent.UpdatedAt)
	if err != nil {
		return generated.InsertOrderIntentParams{}, fmt.Errorf("intent updated at: %w", err)
	}
	if intent.ID == "" || intent.SignalID.Validate() != nil || intent.StrategyID.Validate() != nil ||
		intent.ExchangeAccountID.Validate() != nil || intent.InstrumentID.Validate() != nil ||
		intent.ClientOrderID.Validate() != nil || intent.PayloadChecksum == "" {
		return generated.InsertOrderIntentParams{}, errors.New("invalid order intent identity")
	}
	if err := intent.Quantity.Validate(); err != nil || !intent.Quantity.Value.IsPositive() {
		return generated.InsertOrderIntentParams{}, errors.New("invalid order intent quantity")
	}
	var price, asset sql.NullString
	if intent.LimitPrice != nil {
		if err := intent.LimitPrice.Validate(); err != nil {
			return generated.InsertOrderIntentParams{}, err
		}
		price = sql.NullString{String: intent.LimitPrice.Value.String(), Valid: true}
		asset = sql.NullString{String: intent.LimitPrice.Asset, Valid: true}
	}
	return generated.InsertOrderIntentParams{
		ID: intent.ID, SignalID: string(intent.SignalID), StrategyID: string(intent.StrategyID),
		ExchangeAccountID: string(intent.ExchangeAccountID), InstrumentID: string(intent.InstrumentID),
		ClientOrderID: string(intent.ClientOrderID), Side: int64(intent.Side),
		OrderType: int64(intent.OrderType), Quantity: intent.Quantity.Value.String(),
		LimitPrice: price, PriceAsset: asset, Status: intent.Status,
		PayloadChecksum: intent.PayloadChecksum, CreatedAt: created, UpdatedAt: updated,
	}, nil
}

func exchangeOrderParams(order storage.ExchangeOrder) (generated.InsertExchangeOrderParams, error) {
	if order.ID == "" || order.OrderIntentID == "" || order.ExchangeAccountID.Validate() != nil ||
		order.ExchangeOrderID.Validate() != nil || order.Status == "" {
		return generated.InsertExchangeOrderParams{}, errors.New("invalid exchange order identity")
	}
	if err := order.RequestedQuantity.Validate(); err != nil || !order.RequestedQuantity.Value.IsPositive() {
		return generated.InsertExchangeOrderParams{}, errors.New("invalid requested quantity")
	}
	if err := order.FilledQuantity.Validate(); err != nil ||
		order.FilledQuantity.Value.GreaterThan(order.RequestedQuantity.Value) {
		return generated.InsertExchangeOrderParams{}, errors.New("invalid filled quantity")
	}
	submittedAt, err := micros(order.SubmittedAt)
	if err != nil {
		return generated.InsertExchangeOrderParams{}, fmt.Errorf("order submitted at: %w", err)
	}
	updatedAt, err := micros(order.UpdatedAt)
	if err != nil {
		return generated.InsertExchangeOrderParams{}, fmt.Errorf("order updated at: %w", err)
	}
	var averagePrice, priceAsset sql.NullString
	if order.AveragePrice != nil {
		if err := order.AveragePrice.Validate(); err != nil {
			return generated.InsertExchangeOrderParams{}, err
		}
		averagePrice = sql.NullString{String: order.AveragePrice.Value.String(), Valid: true}
		priceAsset = sql.NullString{String: order.AveragePrice.Asset, Valid: true}
	}
	return generated.InsertExchangeOrderParams{
		ID: order.ID, OrderIntentID: order.OrderIntentID,
		ExchangeAccountID: string(order.ExchangeAccountID),
		ExchangeOrderID:   sql.NullString{String: string(order.ExchangeOrderID), Valid: true},
		Status:            order.Status, RequestedQuantity: order.RequestedQuantity.Value.String(),
		FilledQuantity: order.FilledQuantity.Value.String(), AveragePrice: averagePrice,
		PriceAsset: priceAsset, SubmittedAt: sql.NullInt64{Int64: submittedAt, Valid: true},
		UpdatedAt: updatedAt,
	}, nil
}
