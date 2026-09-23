package config

import (
	"strings"
	"testing"
)

func TestDecimalValidationBoundaries(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name             string
		value            string
		zero             bool
		positiveError    string
		nonNegativeError string
	}{
		{name: "zero", value: "0", zero: true, positiveError: "must be positive"},
		{name: "positive zero", value: "+0.0", zero: true, positiveError: "must be positive"},
		{name: "negative zero", value: "-0.0", zero: true, positiveError: "must be positive"},
		{name: "negative", value: "-1", positiveError: "must not be negative", nonNegativeError: "must not be negative"},
		{name: "positive", value: "1"},
		{name: "greater than uint64", value: "18446744073709551616"},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			if got := isDecimalZero(test.value); got != test.zero {
				t.Fatalf("isDecimalZero(%q) = %t, want %t", test.value, got, test.zero)
			}
			assertDecimalValidationError(t, "positiveDecimal", positiveDecimal(test.value), test.positiveError)
			assertDecimalValidationError(t, "nonNegativeDecimal", nonNegativeDecimal(test.value), test.nonNegativeError)
		})
	}
}

func assertDecimalValidationError(t *testing.T, name string, err error, want string) {
	t.Helper()
	if want == "" {
		if err != nil {
			t.Fatalf("%s() error = %v, want nil", name, err)
		}
		return
	}
	if err == nil || !strings.Contains(err.Error(), want) {
		t.Fatalf("%s() error = %v, want %q", name, err, want)
	}
}
