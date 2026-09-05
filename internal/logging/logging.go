package logging

import (
	"fmt"
	"io"
	"log/slog"
	"os"
	"path/filepath"
	"strings"

	"github.com/damirm/lazytrade/internal/config"
)

// Open creates the application logger and returns a cleanup function for a
// configured file output. Relative file paths are resolved from configDir.
func Open(cfg config.LoggingConfig, configDir string) (*slog.Logger, func() error, error) {
	level, err := parseLevel(cfg.Level)
	if err != nil {
		return nil, nil, err
	}

	var output io.Writer
	closeOutput := func() error { return nil }
	switch cfg.Output {
	case "", "stderr":
		output = os.Stderr
	case "stdout":
		output = os.Stdout
	default:
		path := cfg.Output
		if !filepath.IsAbs(path) {
			path = filepath.Join(configDir, path)
		}
		file, openErr := os.OpenFile(path, os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0o600)
		if openErr != nil {
			return nil, nil, fmt.Errorf("logging.output: open %q: %w", path, openErr)
		}
		output = file
		closeOutput = file.Close
	}

	options := &slog.HandlerOptions{Level: level}
	var handler slog.Handler
	switch cfg.Format {
	case "", "json":
		handler = slog.NewJSONHandler(output, options)
	case "text":
		handler = slog.NewTextHandler(output, options)
	default:
		_ = closeOutput()
		return nil, nil, fmt.Errorf("logging.format: unsupported format %q", cfg.Format)
	}
	return slog.New(handler), closeOutput, nil
}

func parseLevel(value string) (slog.Level, error) {
	switch strings.ToLower(value) {
	case "", "info":
		return slog.LevelInfo, nil
	case "debug":
		return slog.LevelDebug, nil
	case "warn":
		return slog.LevelWarn, nil
	case "error":
		return slog.LevelError, nil
	default:
		return 0, fmt.Errorf("logging.level: unsupported level %q", value)
	}
}
