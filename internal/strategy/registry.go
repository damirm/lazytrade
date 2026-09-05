package strategy

import (
	"encoding/json"
	"errors"
	"fmt"
	"sync"
)

var (
	ErrDuplicateType = errors.New("strategy type already registered")
	ErrUnknownType   = errors.New("unknown strategy type")
)

type Factory interface {
	Type() string
	Build(json.RawMessage) (Strategy, error)
}

type Registry struct {
	mu        sync.RWMutex
	factories map[string]Factory
}

func NewRegistry() *Registry {
	return &Registry{factories: make(map[string]Factory)}
}

func (r *Registry) Register(factory Factory) error {
	if factory == nil || factory.Type() == "" {
		return errors.New("strategy factory and type are required")
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	if _, exists := r.factories[factory.Type()]; exists {
		return fmt.Errorf("%w: %s", ErrDuplicateType, factory.Type())
	}
	r.factories[factory.Type()] = factory
	return nil
}

func (r *Registry) Build(strategyType string, config json.RawMessage) (Strategy, error) {
	r.mu.RLock()
	factory, exists := r.factories[strategyType]
	r.mu.RUnlock()
	if !exists {
		return nil, fmt.Errorf("%w: %s", ErrUnknownType, strategyType)
	}
	return factory.Build(config)
}
