package config

import (
	"bytes"
	"errors"
	"fmt"
	"io"
	"os"
)

// LoadFile reads, strictly decodes, and statically validates a configuration.
func LoadFile(path string) (Config, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return Config{}, fmt.Errorf("read config %q: %w", path, err)
	}

	cfg, err := Load(data)
	if err != nil {
		return Config{}, fmt.Errorf("load config %q: %w", path, err)
	}
	return cfg, nil
}

// LoadFileFor loads a configuration and applies command-scoped environment
// validation. Backtest validation never resolves exchange credentials.
func LoadFileFor(path string, command Command) (Config, error) {
	cfg, err := LoadFile(path)
	if err != nil {
		return Config{}, err
	}
	if err := cfg.ValidateFor(command, nil); err != nil {
		return Config{}, err
	}
	return cfg, nil
}

// Load strictly decodes one YAML document and validates its schema version.
func Load(data []byte) (Config, error) {
	decoder := newYAMLDecoder(bytes.NewReader(data))

	var cfg Config
	if err := decoder.Decode(&cfg); err != nil {
		if errors.Is(err, io.EOF) {
			return Config{}, errors.New("config: empty document")
		}
		return Config{}, fmt.Errorf("config: decode YAML: %w", err)
	}

	var extra any
	if err := decoder.Decode(&extra); !errors.Is(err, io.EOF) {
		if err != nil {
			return Config{}, fmt.Errorf("config: decode trailing YAML: %w", err)
		}
		return Config{}, errors.New("config: multiple YAML documents are not allowed")
	}

	if err := cfg.Validate(); err != nil {
		return Config{}, err
	}
	return cfg, nil
}
