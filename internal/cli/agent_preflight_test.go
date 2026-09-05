package cli

import (
	"strings"
	"testing"

	"github.com/damirm/lazytrade/internal/domain"
	"github.com/shopspring/decimal"
)

func TestValidateWholeLots(t *testing.T) {
	lot := domain.Quantity{Value: decimal.NewFromInt(10)}
	if err := validateWholeLots(domain.Quantity{Value: decimal.NewFromInt(20)}, lot); err != nil {
		t.Fatal(err)
	}
	err := validateWholeLots(domain.Quantity{Value: decimal.NewFromInt(1)}, lot)
	if err == nil || !strings.Contains(err.Error(), "lot size is 10") {
		t.Fatalf("error = %v", err)
	}
}

func TestAgentCommandExposesPreflight(t *testing.T) {
	command := newAgentCommand()
	for _, child := range command.Commands() {
		if child.Name() == "preflight" {
			return
		}
	}
	t.Fatal("agent preflight subcommand is missing")
}
