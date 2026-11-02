package config

import (
	"strings"
	"testing"
)

func TestPublicEmailUnionConfiguration(t *testing.T) {
	for _, class := range []string{"free", "plus", "pro", "shared"} {
		t.Run(class, func(t *testing.T) {
			t.Setenv("AGENT_RESOURCE_CLASS", class)
			t.Setenv("AGENT_CUSTOM_EMAILS", `[" Alice@Example.COM ","alice@example.com","bob@example.com"]`)
			cfg, err := LoadReader(strings.NewReader("mode: agent"))
			if err != nil {
				t.Fatal(err)
			}
			if len(cfg.Agent.AllowedEmails) != 2 || cfg.Agent.AllowedEmails[0] != "alice@example.com" {
				t.Fatal("emails not canonical")
			}
		})
	}
	for _, value := range []string{`"*@example.com"`, `["alice*@example.com"]`} {
		t.Run(value, func(t *testing.T) {
			t.Setenv("AGENT_RESOURCE_CLASS", "plus")
			t.Setenv("AGENT_CUSTOM_EMAILS", value)
			if _, err := LoadReader(strings.NewReader("mode: agent")); err == nil {
				t.Fatal("wildcard accepted")
			}
		})
	}
	t.Run("team", func(t *testing.T) {
		t.Setenv("AGENT_RESOURCE_CLASS", "team")
		t.Setenv("AGENT_CUSTOM_EMAILS", "alice@example.com")
		if _, err := LoadReader(strings.NewReader("mode: agent")); err == nil {
			t.Fatal("team extension accepted")
		}
	})
	t.Run("single email", func(t *testing.T) {
		t.Setenv("AGENT_RESOURCE_CLASS", "plus")
		t.Setenv("AGENT_CUSTOM_EMAILS", " Alice@Example.com ")
		if _, err := LoadReader(strings.NewReader("mode: agent")); err != nil {
			t.Fatal(err)
		}
	})
}
