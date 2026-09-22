package config

import (
	"strings"
	"testing"
)

func TestLoadDefaultsToAgentProxyRole(t *testing.T) {
	cfg, err := LoadReader(strings.NewReader(`mode: agent
agent:
  id: node-1
  controllerUrl: https://accounts.example.test
  apiToken: token
`))
	if err != nil {
		t.Fatal(err)
	}
	if got := cfg.Agent.EffectiveRole(); got != RoleAgentProxy {
		t.Fatalf("role = %q, want %q", got, RoleAgentProxy)
	}
}

func TestLoadAcceptsSupportedRoles(t *testing.T) {
	for _, role := range []string{RoleGateway, RoleOne, RoleAgentProxy} {
		cfg, err := LoadReader(strings.NewReader("mode: agent\nagent:\n  role: " + role + "\n  controllerUrl: https://accounts.example.test\n  apiToken: token\n"))
		if err != nil {
			t.Fatalf("role %q: %v", role, err)
		}
		if cfg.Agent.EffectiveRole() != role {
			t.Fatalf("role = %q, want %q", cfg.Agent.EffectiveRole(), role)
		}
	}
}

func TestLoadRejectsUnknownRole(t *testing.T) {
	_, err := LoadReader(strings.NewReader(`mode: agent
agent:
  role: unknown
  controllerUrl: https://accounts.example.test
  apiToken: token
`))
	if err == nil {
		t.Fatal("expected unknown role to be rejected")
	}
}
