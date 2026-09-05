package logging

import (
	"log/slog"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/damirm/lazytrade/internal/config"
)

func TestOpenWritesConfiguredJSONFileAndFiltersLevel(t *testing.T) {
	dir := t.TempDir()
	logger, closeLogger, err := Open(config.LoggingConfig{Level: "info", Format: "json", Output: "agent.log"}, dir)
	if err != nil {
		t.Fatal(err)
	}
	logger.Debug("hidden", "event", "debug_event")
	logger.Info("started", "event", "agent_running")
	if err := closeLogger(); err != nil {
		t.Fatal(err)
	}
	data, err := os.ReadFile(filepath.Join(dir, "agent.log"))
	if err != nil {
		t.Fatal(err)
	}
	output := string(data)
	if !strings.Contains(output, `"event":"agent_running"`) || strings.Contains(output, "debug_event") {
		t.Fatalf("unexpected log output: %s", output)
	}
}

func TestOpenRejectsInvalidConfiguration(t *testing.T) {
	for _, cfg := range []config.LoggingConfig{{Level: "trace"}, {Format: "xml"}} {
		logger, closeLogger, err := Open(cfg, t.TempDir())
		if err == nil || logger != nil || closeLogger != nil {
			t.Fatalf("Open(%+v) returned logger=%v cleanup_present=%v error=%v; want only error", cfg, logger, closeLogger != nil, err)
		}
	}
}

func TestParseLevel(t *testing.T) {
	level, err := parseLevel("warn")
	if err != nil || level != slog.LevelWarn {
		t.Fatalf("parseLevel(warn) = %v, %v", level, err)
	}
}
