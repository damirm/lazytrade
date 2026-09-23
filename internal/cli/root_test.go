package cli

import (
	"bytes"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestRootContainsImplementedCommands(t *testing.T) {
	t.Parallel()

	root := newRootCommand()
	for _, path := range [][]string{
		{"version"},
		{"agent"},
		{"backtest"},
		{"data", "download"},
		{"data", "validate"},
		{"config", "validate"},
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

func TestRootHelp(t *testing.T) {
	t.Parallel()

	want, err := os.ReadFile(filepath.Join("testdata", "root_help.golden"))
	if err != nil {
		t.Fatalf("ReadFile() error = %v", err)
	}

	root := newRootCommand()
	var output bytes.Buffer
	root.SetOut(&output)
	root.SetArgs([]string{"--help"})

	if err := root.Execute(); err != nil {
		t.Fatalf("Execute() error = %v", err)
	}
	if got := output.String(); got != string(want) {
		t.Errorf("root help differs from golden\n got: %q\nwant: %q", got, want)
	}
}

func TestRootRejectsUnimplementedCommands(t *testing.T) {
	t.Parallel()

	for _, args := range [][]string{
		{"terminal"},
		{"db", "migrate"},
	} {
		t.Run(strings.Join(args, " "), func(t *testing.T) {
			root := newRootCommand()
			root.SetArgs(args)

			err := root.Execute()
			if err == nil {
				t.Fatal("Execute() error = nil, want unknown command error")
			}
			if !strings.Contains(err.Error(), "unknown command") {
				t.Fatalf("Execute() error = %q, want unknown command error", err)
			}
		})
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
