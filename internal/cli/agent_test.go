package cli

import (
	"context"
	"strings"
	"testing"

	appconfig "github.com/damirm/lazytrade/internal/config"
)

func TestRunSandboxAgentHonorsEmergencyStopBeforeExternalAccess(t *testing.T) {
	err := runAgent(context.Background(), "config.yaml", appconfig.Config{
		Agent: appconfig.AgentConfig{EmergencyStop: true},
	})
	if err == nil || !strings.Contains(err.Error(), "emergency_stop") {
		t.Fatalf("error = %v", err)
	}
}

func TestRunSandboxAgentRequiresAtLeastOneStrategyBeforeExternalAccess(t *testing.T) {
	err := runAgent(context.Background(), "config.yaml", appconfig.Config{})
	if err == nil || !strings.Contains(err.Error(), "at least one strategy") {
		t.Fatalf("error = %v", err)
	}
}

func TestConfiguredValuePrefersEnvironment(t *testing.T) {
	t.Setenv("LAZYTRADE_TEST_DSN", "from-env")
	value, err := configuredValue("literal", "LAZYTRADE_TEST_DSN", "DSN")
	if err != nil || value != "from-env" {
		t.Fatalf("value=%q error=%v", value, err)
	}
}
