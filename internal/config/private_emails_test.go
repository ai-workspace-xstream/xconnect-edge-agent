package config

import (
	"strings"
	"testing"
)

func TestAllowedEmailsEnvironment(t *testing.T) {
	t.Setenv("AGENT_RESOURCE_CLASS", "custom")
	t.Setenv("AGENT_CUSTOM_EMAILS", `["Alice@example.com","bob@example.com"]`)
	cfg, err := LoadReader(strings.NewReader("agent: {}"))
	if err != nil || len(cfg.Agent.AllowedEmails) != 2 {
		t.Fatalf("config=%+v err=%v", cfg, err)
	}
	t.Setenv("AGENT_RESOURCE_CLASS", "custom")
	t.Setenv("AGENT_CUSTOM_EMAILS", `[]`)
	if _, err := LoadReader(strings.NewReader("agent: {}")); err == nil {
		t.Fatal("empty explicit allowlist must fail")
	}
	t.Setenv("AGENT_CUSTOM_EMAILS", `["invalid"]`)
	if _, err := LoadReader(strings.NewReader("agent: {}")); err == nil {
		t.Fatal("invalid email must fail")
	}
}

func TestCustomSingleEmailEnvironment(t *testing.T) {
	t.Setenv("AGENT_RESOURCE_CLASS", "custom")
	t.Setenv("AGENT_CUSTOM_EMAILS", "alice@example.com")
	cfg, err := LoadReader(strings.NewReader("agent: {}"))
	if err != nil || len(cfg.Agent.AllowedEmails) != 1 || cfg.Agent.AllowedEmails[0] != "alice@example.com" {
		t.Fatalf("config=%+v err=%v", cfg, err)
	}
}
