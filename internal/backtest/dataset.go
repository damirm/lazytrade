package backtest

import (
	"context"
	"crypto/sha256"
	"encoding/csv"
	"encoding/hex"
	"errors"
	"fmt"
	"hash"
	"io"
	"strconv"
	"strings"
	"time"

	"github.com/damirm/lazytrade/internal/domain"
)

type GapPolicy string

const (
	GapFail  GapPolicy = "fail"
	GapAllow GapPolicy = "allow"
	GapMark  GapPolicy = "mark"
)

type DatasetMetadata struct {
	Version           uint32
	ExchangeAccountID domain.ExchangeAccountID
	InstrumentID      domain.InstrumentID
	Interval          time.Duration
	PriceAsset        string
	Timezone          *time.Location
	TimestampLayout   string
	TickSize          domain.Price
	LotSize           domain.Quantity
	GapPolicy         GapPolicy
	ExpectedSHA256    string
}

func (m DatasetMetadata) Validate() error {
	if m.Version != 1 {
		return fmt.Errorf("unsupported dataset metadata version %d", m.Version)
	}
	if err := m.ExchangeAccountID.Validate(); err != nil {
		return fmt.Errorf("exchange account ID: %w", err)
	}
	if err := m.InstrumentID.Validate(); err != nil {
		return fmt.Errorf("instrument ID: %w", err)
	}
	if m.Interval <= 0 {
		return errors.New("interval must be positive")
	}
	asset, err := domain.NormalizeAsset(m.PriceAsset)
	if err != nil || asset != m.PriceAsset {
		return errors.New("price asset must be normalized")
	}
	if m.Timezone == nil || m.TimestampLayout == "" {
		return errors.New("timezone and timestamp layout are required")
	}
	if err := m.TickSize.Validate(); err != nil || m.TickSize.Asset != m.PriceAsset {
		return errors.New("tick size must be positive and use price asset")
	}
	if err := m.LotSize.Validate(); err != nil || !m.LotSize.Value.IsPositive() {
		return errors.New("lot size must be positive")
	}
	switch m.GapPolicy {
	case GapFail, GapAllow, GapMark:
	default:
		return errors.New("gap policy must be fail, allow, or mark")
	}
	if m.ExpectedSHA256 != "" {
		decoded, err := hex.DecodeString(m.ExpectedSHA256)
		if err != nil || len(decoded) != sha256.Size {
			return errors.New("expected SHA-256 must be 64 hexadecimal characters")
		}
	}
	return nil
}

type Gap struct {
	Previous time.Time `json:"previous"`
	Current  time.Time `json:"current"`
	Missing  uint64    `json:"missing"`
}

type EventIterator interface {
	Next(context.Context) (domain.MarketEvent, error)
}

type CSVIterator struct {
	reader   *csv.Reader
	hash     hash.Hash
	meta     DatasetMetadata
	header   bool
	row      uint64
	first    time.Time
	previous time.Time
	gaps     []Gap
	done     bool
}

func NewCSVIterator(input io.Reader, metadata DatasetMetadata) (*CSVIterator, error) {
	if input == nil {
		return nil, errors.New("dataset reader is required")
	}
	if err := metadata.Validate(); err != nil {
		return nil, fmt.Errorf("metadata: %w", err)
	}
	digest := sha256.New()
	reader := csv.NewReader(io.TeeReader(input, digest))
	reader.FieldsPerRecord = 6
	reader.ReuseRecord = true
	return &CSVIterator{reader: reader, hash: digest, meta: metadata}, nil
}

func (i *CSVIterator) Next(ctx context.Context) (domain.MarketEvent, error) {
	if i.done {
		return domain.MarketEvent{}, io.EOF
	}
	if err := ctx.Err(); err != nil {
		return domain.MarketEvent{}, err
	}
	if !i.header {
		record, err := i.reader.Read()
		if err != nil {
			return domain.MarketEvent{}, fmt.Errorf("read CSV header: %w", err)
		}
		expected := []string{"timestamp", "open", "high", "low", "close", "volume"}
		for index := range expected {
			if strings.TrimSpace(record[index]) != expected[index] {
				return domain.MarketEvent{}, fmt.Errorf("CSV header column %d must be %q", index+1, expected[index])
			}
		}
		i.header = true
	}
	record, err := i.reader.Read()
	if errors.Is(err, io.EOF) {
		i.done = true
		if i.meta.ExpectedSHA256 != "" {
			actual := hex.EncodeToString(i.hash.Sum(nil))
			if actual != strings.ToLower(i.meta.ExpectedSHA256) {
				return domain.MarketEvent{}, fmt.Errorf("dataset checksum mismatch: got %s", actual)
			}
		}
		return domain.MarketEvent{}, io.EOF
	}
	if err != nil {
		return domain.MarketEvent{}, fmt.Errorf("read CSV row %d: %w", i.row+2, err)
	}
	i.row++
	timestamp, err := time.ParseInLocation(i.meta.TimestampLayout, strings.TrimSpace(record[0]), i.meta.Timezone)
	if err != nil {
		return domain.MarketEvent{}, fmt.Errorf("row %d timestamp: %w", i.row+1, err)
	}
	start := timestamp.UTC()
	if i.first.IsZero() {
		i.first = start
	}
	if !i.previous.IsZero() {
		delta := start.Sub(i.previous)
		if delta <= 0 {
			return domain.MarketEvent{}, fmt.Errorf("row %d timestamp must be strictly increasing", i.row+1)
		}
		if delta != i.meta.Interval {
			if delta%i.meta.Interval != 0 {
				return domain.MarketEvent{}, fmt.Errorf("row %d timestamp does not align to interval", i.row+1)
			}
			gap := Gap{Previous: i.previous, Current: start, Missing: uint64(delta/i.meta.Interval) - 1}
			if i.meta.GapPolicy == GapFail {
				return domain.MarketEvent{}, fmt.Errorf("row %d contains gap of %d candles", i.row+1, gap.Missing)
			}
			if i.meta.GapPolicy == GapMark {
				i.gaps = append(i.gaps, gap)
			}
		}
	}
	prices := make([]domain.Price, 4)
	for index := range prices {
		prices[index], err = domain.NewPrice(strings.TrimSpace(record[index+1]), i.meta.PriceAsset)
		if err != nil {
			return domain.MarketEvent{}, fmt.Errorf("row %d column %d: %w", i.row+1, index+2, err)
		}
		if !prices[index].Value.Mod(i.meta.TickSize.Value).IsZero() {
			return domain.MarketEvent{}, fmt.Errorf("row %d column %d is not aligned to tick size", i.row+1, index+2)
		}
	}
	volume, err := domain.NewQuantity(strings.TrimSpace(record[5]))
	if err != nil {
		return domain.MarketEvent{}, fmt.Errorf("row %d volume: %w", i.row+1, err)
	}
	if !volume.Value.Mod(i.meta.LotSize.Value).IsZero() {
		return domain.MarketEvent{}, fmt.Errorf("row %d volume is not aligned to lot size", i.row+1)
	}
	candle := domain.Candle{
		Start: start, End: start.Add(i.meta.Interval), Interval: i.meta.Interval,
		Open: prices[0], High: prices[1], Low: prices[2], Close: prices[3],
		Volume: volume, Complete: true,
	}
	if err := candle.Validate(); err != nil {
		return domain.MarketEvent{}, fmt.Errorf("row %d candle: %w", i.row+1, err)
	}
	i.previous = start
	return domain.MarketEvent{
		ExchangeAccountID: i.meta.ExchangeAccountID, InstrumentID: i.meta.InstrumentID,
		Kind: domain.MarketEventCandleClose, ExchangeTime: candle.End, ReceivedTime: candle.End,
		Sequence: i.row, Candle: &candle,
	}, nil
}

func (i *CSVIterator) Checksum() (string, error) {
	if !i.done {
		return "", errors.New("checksum is available only after iterator reaches EOF")
	}
	return hex.EncodeToString(i.hash.Sum(nil)), nil
}

func (i *CSVIterator) Gaps() []Gap {
	return append([]Gap(nil), i.gaps...)
}

func (i *CSVIterator) Rows() uint64     { return i.row }
func (i *CSVIterator) First() time.Time { return i.first }
func (i *CSVIterator) Last() time.Time  { return i.previous }

func ParseSeed(value string) (uint64, error) {
	return strconv.ParseUint(value, 10, 64)
}
