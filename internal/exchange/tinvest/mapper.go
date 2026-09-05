package tinvest

import (
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/damirm/lazytrade/internal/domain"
	"github.com/shopspring/decimal"
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

func mapStatus(status pb.SecurityTradingStatus) domain.TradingStatus {
	switch status {
	case pb.SecurityTradingStatus_SECURITY_TRADING_STATUS_NORMAL_TRADING:
		return domain.TradingStatusOpen
	case pb.SecurityTradingStatus_SECURITY_TRADING_STATUS_OPENING_PERIOD:
		return domain.TradingStatusOpening
	case pb.SecurityTradingStatus_SECURITY_TRADING_STATUS_CLOSING_PERIOD:
		return domain.TradingStatusClosing
	case pb.SecurityTradingStatus_SECURITY_TRADING_STATUS_NOT_AVAILABLE_FOR_TRADING,
		pb.SecurityTradingStatus_SECURITY_TRADING_STATUS_DEALER_NORMAL_TRADING:
		return domain.TradingStatusUnavailable
	default:
		return domain.TradingStatusClosed
	}
}

func utc(t time.Time) time.Time { return t.UTC() }
