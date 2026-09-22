package tinvest

import (
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/damirm/lazytrade/internal/domain"
	"github.com/shopspring/decimal"
	"google.golang.org/protobuf/types/known/timestamppb"
	pb "opensource.tbank.ru/invest/invest-go/proto"
)

var errMissingValue = errors.New("T-Invest value is missing")

func quotation(q *pb.Quotation) (decimal.Decimal, error) {
	if q == nil {
		return decimal.Zero, errMissingValue
	}
	if q.Nano <= -1_000_000_000 || q.Nano >= 1_000_000_000 {
		return decimal.Zero, fmt.Errorf("invalid nanos %d", q.Nano)
	}
	return decimal.New(q.Units, 0).Add(decimal.New(int64(q.Nano), -9)), nil
}

func money(v *pb.MoneyValue) (domain.Money, error) {
	if v == nil {
		return domain.Money{}, errMissingValue
	}
	asset, err := domain.NormalizeAsset(v.Currency)
	if err != nil {
		return domain.Money{}, err
	}
	value, err := quotation(&pb.Quotation{Units: v.Units, Nano: v.Nano})
	if err != nil {
		return domain.Money{}, err
	}
	return domain.Money{Amount: value, Asset: asset}, nil
}

func price(q *pb.Quotation, asset string) (domain.Price, error) {
	value, err := quotation(q)
	if err != nil {
		return domain.Price{}, err
	}
	normalized, err := domain.NormalizeAsset(asset)
	if err != nil {
		return domain.Price{}, err
	}
	p := domain.Price{Value: value, Asset: normalized}
	return p, p.Validate()
}

func mapInstrument(account domain.ExchangeAccountID, item *pb.Instrument) (domain.Instrument, error) {
	if item == nil {
		return domain.Instrument{}, errors.New("instrument is missing")
	}
	asset, err := domain.NormalizeAsset(item.Currency)
	if err != nil {
		return domain.Instrument{}, err
	}
	step, err := price(item.MinPriceIncrement, asset)
	if err != nil {
		return domain.Instrument{}, fmt.Errorf("minimum price increment: %w", err)
	}
	if item.Lot <= 0 {
		return domain.Instrument{}, fmt.Errorf("invalid lot %d", item.Lot)
	}
	instrument := domain.Instrument{
		ID: domain.InstrumentID(item.Uid), ExchangeAccount: account,
		Symbol: item.Ticker, Name: item.Name, BaseAsset: strings.TrimSpace(item.Ticker),
		QuoteAsset: asset, SettlementAsset: asset, PriceStep: step,
		QuantityStep: domain.Quantity{Value: decimal.NewFromInt(int64(item.Lot))},
		MinQuantity:  domain.Quantity{Value: decimal.NewFromInt(int64(item.Lot))},
	}
	return instrument, instrument.Validate()
}

func mapShare(account domain.ExchangeAccountID, item *pb.Share) (domain.Instrument, error) {
	if item == nil {
		return domain.Instrument{}, errors.New("share is missing")
	}
	return mapInstrument(account, &pb.Instrument{
		Uid: item.Uid, Ticker: item.Ticker, Name: item.Name, Currency: item.Currency,
		Lot: item.Lot, MinPriceIncrement: item.MinPriceIncrement,
	})
}

func mapStatus(status pb.SecurityTradingStatus) (domain.TradingStatus, error) {
	switch status {
	case pb.SecurityTradingStatus_SECURITY_TRADING_STATUS_NORMAL_TRADING:
		return domain.TradingStatusOpen, nil
	case pb.SecurityTradingStatus_SECURITY_TRADING_STATUS_OPENING_PERIOD:
		return domain.TradingStatusOpening, nil
	case pb.SecurityTradingStatus_SECURITY_TRADING_STATUS_CLOSING_PERIOD:
		return domain.TradingStatusClosing, nil
	case pb.SecurityTradingStatus_SECURITY_TRADING_STATUS_NOT_AVAILABLE_FOR_TRADING,
		pb.SecurityTradingStatus_SECURITY_TRADING_STATUS_DEALER_NORMAL_TRADING:
		return domain.TradingStatusUnavailable, nil
	case pb.SecurityTradingStatus_SECURITY_TRADING_STATUS_BREAK_IN_TRADING,
		pb.SecurityTradingStatus_SECURITY_TRADING_STATUS_CLOSING_AUCTION,
		pb.SecurityTradingStatus_SECURITY_TRADING_STATUS_DARK_POOL_AUCTION,
		pb.SecurityTradingStatus_SECURITY_TRADING_STATUS_DISCRETE_AUCTION,
		pb.SecurityTradingStatus_SECURITY_TRADING_STATUS_OPENING_AUCTION_PERIOD,
		pb.SecurityTradingStatus_SECURITY_TRADING_STATUS_TRADING_AT_CLOSING_AUCTION_PRICE,
		pb.SecurityTradingStatus_SECURITY_TRADING_STATUS_SESSION_ASSIGNED,
		pb.SecurityTradingStatus_SECURITY_TRADING_STATUS_SESSION_CLOSE,
		pb.SecurityTradingStatus_SECURITY_TRADING_STATUS_SESSION_OPEN,
		pb.SecurityTradingStatus_SECURITY_TRADING_STATUS_DEALER_BREAK_IN_TRADING,
		pb.SecurityTradingStatus_SECURITY_TRADING_STATUS_DEALER_NOT_AVAILABLE_FOR_TRADING,
		pb.SecurityTradingStatus_SECURITY_TRADING_STATUS_STABILIZATION_AUCTION:
		// These known states do not enable normal trading in the current adapter.
		return domain.TradingStatusClosed, nil
	default:
		return 0, fmt.Errorf("unsupported trading status %d", status)
	}
}

func requiredTime(value *timestamppb.Timestamp) (time.Time, error) {
	if value == nil {
		return time.Time{}, fmt.Errorf("timestamp: %w", errMissingValue)
	}
	if err := value.CheckValid(); err != nil {
		return time.Time{}, fmt.Errorf("invalid timestamp: %w", err)
	}
	result := value.AsTime().UTC()
	if result.IsZero() {
		return time.Time{}, errors.New("timestamp must be non-zero")
	}
	return result, nil
}

func tradeSide(value pb.TradeDirection) (domain.OrderSide, error) {
	switch value {
	case pb.TradeDirection_TRADE_DIRECTION_BUY:
		return domain.OrderSideBuy, nil
	case pb.TradeDirection_TRADE_DIRECTION_SELL:
		return domain.OrderSideSell, nil
	default:
		return 0, fmt.Errorf("unsupported trade direction %d", value)
	}
}

func mapOHLC(candle *domain.Candle, asset string, open, high, low, close *pb.Quotation) error {
	for _, field := range []struct {
		name   string
		source *pb.Quotation
		target *domain.Price
	}{
		{"open", open, &candle.Open},
		{"high", high, &candle.High},
		{"low", low, &candle.Low},
		{"close", close, &candle.Close},
	} {
		value, err := price(field.source, asset)
		if err != nil {
			return fmt.Errorf("%s price: %w", field.name, err)
		}
		*field.target = value
	}
	return nil
}
