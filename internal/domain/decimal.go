package domain

import (
	"errors"
	"fmt"
	"regexp"
	"strings"

	"github.com/shopspring/decimal"
)

var (
	ErrEmptyAsset       = errors.New("asset must not be empty")
	ErrInvalidAsset     = errors.New("asset contains unsupported characters")
	ErrAssetMismatch    = errors.New("asset mismatch")
	ErrNegativeQuantity = errors.New("quantity must not be negative")
	ErrNonPositivePrice = errors.New("price must be positive")
)

var assetPattern = regexp.MustCompile(`^[A-Z0-9][A-Z0-9._-]{0,31}$`)

type Money struct {
	Amount decimal.Decimal
	Asset  string
}

type Price struct {
	Value decimal.Decimal
	Asset string
}

type Quantity struct {
	Value decimal.Decimal
}

func NormalizeAsset(asset string) (string, error) {
	normalized := strings.ToUpper(strings.TrimSpace(asset))
	if normalized == "" {
		return "", ErrEmptyAsset
	}
	if !assetPattern.MatchString(normalized) {
		return "", fmt.Errorf("%w: %q", ErrInvalidAsset, asset)
	}
	return normalized, nil
}

func NewMoney(amount, asset string) (Money, error) {
	value, err := decimal.NewFromString(amount)
	if err != nil {
		return Money{}, fmt.Errorf("parse money amount: %w", err)
	}
	normalized, err := NormalizeAsset(asset)
	if err != nil {
		return Money{}, err
	}
	return Money{Amount: value, Asset: normalized}, nil
}

func (m Money) Validate() error {
	normalized, err := NormalizeAsset(m.Asset)
	if err != nil {
		return err
	}
	if normalized != m.Asset {
		return fmt.Errorf("asset is not normalized: %q", m.Asset)
	}
	return nil
}

func (m Money) Add(other Money) (Money, error) {
	if err := matchingAssets(m.Asset, other.Asset); err != nil {
		return Money{}, err
	}
	return Money{Amount: m.Amount.Add(other.Amount), Asset: m.Asset}, nil
}

func (m Money) Sub(other Money) (Money, error) {
	if err := matchingAssets(m.Asset, other.Asset); err != nil {
		return Money{}, err
	}
	return Money{Amount: m.Amount.Sub(other.Amount), Asset: m.Asset}, nil
}

func (m Money) Cmp(other Money) (int, error) {
	if err := matchingAssets(m.Asset, other.Asset); err != nil {
		return 0, err
	}
	return m.Amount.Cmp(other.Amount), nil
}

func NewPrice(value, asset string) (Price, error) {
	amount, err := decimal.NewFromString(value)
	if err != nil {
		return Price{}, fmt.Errorf("parse price: %w", err)
	}
	if !amount.IsPositive() {
		return Price{}, ErrNonPositivePrice
	}
	normalized, err := NormalizeAsset(asset)
	if err != nil {
		return Price{}, err
	}
	return Price{Value: amount, Asset: normalized}, nil
}

func (p Price) Validate() error {
	if !p.Value.IsPositive() {
		return ErrNonPositivePrice
	}
	normalized, err := NormalizeAsset(p.Asset)
	if err != nil {
		return err
	}
	if normalized != p.Asset {
		return fmt.Errorf("asset is not normalized: %q", p.Asset)
	}
	return nil
}

func (p Price) Cmp(other Price) (int, error) {
	if err := matchingAssets(p.Asset, other.Asset); err != nil {
		return 0, err
	}
	return p.Value.Cmp(other.Value), nil
}

func NewQuantity(value string) (Quantity, error) {
	amount, err := decimal.NewFromString(value)
	if err != nil {
		return Quantity{}, fmt.Errorf("parse quantity: %w", err)
	}
	if amount.IsNegative() {
		return Quantity{}, ErrNegativeQuantity
	}
	return Quantity{Value: amount}, nil
}

func (q Quantity) Validate() error {
	if q.Value.IsNegative() {
		return ErrNegativeQuantity
	}
	return nil
}

func matchingAssets(left, right string) error {
	if err := (&Money{Asset: left}).validateAssetOnly(); err != nil {
		return err
	}
	if err := (&Money{Asset: right}).validateAssetOnly(); err != nil {
		return err
	}
	if left != right {
		return fmt.Errorf("%w: %s and %s", ErrAssetMismatch, left, right)
	}
	return nil
}

func (m *Money) validateAssetOnly() error {
	normalized, err := NormalizeAsset(m.Asset)
	if err != nil {
		return err
	}
	if normalized != m.Asset {
		return fmt.Errorf("asset is not normalized: %q", m.Asset)
	}
	return nil
}
