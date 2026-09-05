package cli

import (
	"bytes"
	"strings"
	"testing"
)

func TestRootContainsRequiredCommands(t *testing.T) {
	t.Parallel()

	root := newRootCommand()
	for _, path := range [][]string{
		{"version"},
		{"agent"},
		{"terminal"},
		{"backtest"},
		{"data", "download"},
		{"data", "validate"},
		{"config", "validate"},
		{"db", "migrate"},
	} {
		command, _, err := root.Find(path)
		if err != nil {
			t.Errorf("Find(%v) error = %v", path, err)
			continue
		}
		if command == root {
			t.Errorf("Find(%v) returned root command", path)
		}
	}
}

func TestVersion(t *testing.T) {
	t.Parallel()

	root := newRootCommand()
	var output bytes.Buffer
	root.SetOut(&output)
	root.SetArgs([]string{"version"})

	if err := root.Execute(); err != nil {
		t.Fatalf("Execute() error = %v", err)
	}
	if !strings.Contains(output.String(), "lazytrade dev") {
		t.Fatalf("output = %q, want development version", output.String())
	}
}
