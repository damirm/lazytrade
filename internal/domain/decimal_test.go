package domain

import (
	"errors"
	"testing"
)

func TestNormalizeAsset(t *testing.T) {
	tests := []struct {
		name    string
		input   string
		want    string
		wantErr error
	}{
		{name: "uppercase", input: " rub ", want: "RUB"},
		{name: "stablecoin", input: "usdt", want: "USDT"},
		{name: "empty", input: "  ", wantErr: ErrEmptyAsset},
		{name: "spaces", input: "US DT", wantErr: ErrInvalidAsset},
		{name: "too long", input: "ABCDEFGHIJKLMNOPQRSTUVWXYZ1234567", wantErr: ErrInvalidAsset},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := NormalizeAsset(tt.input)
			if !errors.Is(err, tt.wantErr) {
				t.Fatalf("NormalizeAsset error = %v, want %v", err, tt.wantErr)
			}
			if got != tt.want {
				t.Fatalf("NormalizeAsset = %q, want %q", got, tt.want)
			}
		})
	}
}

func TestMoneyArithmetic(t *testing.T) {
	rubles, err := NewMoney("10.25", "rub")
	if err != nil {
		t.Fatal(err)
	}
	moreRubles, _ := NewMoney("0.75", "RUB")
	dollars, _ := NewMoney("1", "USD")

	sum, err := rubles.Add(moreRubles)
	if err != nil {
		t.Fatal(err)
	}
	if got := sum.Amount.String(); got != "11" {
		t.Fatalf("sum = %s, want 11", got)
	}

	operations := []struct {
		name string
		run  func() error
	}{
		{"add", func() error { _, err := rubles.Add(dollars); return err }},
		{"subtract", func() error { _, err := rubles.Sub(dollars); return err }},
		{"compare", func() error { _, err := rubles.Cmp(dollars); return err }},
	}
	for _, operation := range operations {
		t.Run(operation.name, func(t *testing.T) {
			if err := operation.run(); !errors.Is(err, ErrAssetMismatch) {
				t.Fatalf("error = %v, want ErrAssetMismatch", err)
			}
		})
	}
}

func TestDecimalConstructorsRejectInvalidValues(t *testing.T) {
	tests := []struct {
		name string
		run  func() error
	}{
		{"invalid money", func() error { _, err := NewMoney("NaN", "RUB"); return err }},
		{"zero price", func() error { _, err := NewPrice("0", "RUB"); return err }},
		{"negative price", func() error { _, err := NewPrice("-1", "RUB"); return err }},
		{"negative quantity", func() error { _, err := NewQuantity("-0.1"); return err }},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if err := tt.run(); err == nil {
				t.Fatal("expected error")
			}
		})
	}
}
