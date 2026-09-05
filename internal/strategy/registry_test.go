package strategy_test

import (
	"encoding/json"
	"errors"
	"testing"

	"github.com/damirm/lazytrade/internal/strategy"
	"github.com/damirm/lazytrade/internal/strategy/movingaverage"
)

func TestRegistry(t *testing.T) {
	registry := strategy.NewRegistry()
	if err := registry.Register(movingaverage.Factory{}); err != nil {
		t.Fatal(err)
	}
	if err := registry.Register(movingaverage.Factory{}); !errors.Is(err, strategy.ErrDuplicateType) {
		t.Fatalf("duplicate error = %v", err)
	}
	built, err := registry.Build(movingaverage.Type, json.RawMessage(`{"fast_period":2,"slow_period":3,"interval":"1m","quantity":"1"}`))
	if err != nil {
		t.Fatal(err)
	}
	if built.Type() != movingaverage.Type {
		t.Fatalf("type = %s", built.Type())
	}
	if _, err := registry.Build("missing", json.RawMessage(`{}`)); !errors.Is(err, strategy.ErrUnknownType) {
		t.Fatalf("unknown error = %v", err)
	}
}
