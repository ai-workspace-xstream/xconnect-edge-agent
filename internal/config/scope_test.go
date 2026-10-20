package config

import (
	"strings"
	"testing"
)

func TestUserScopeValidation(t *testing.T) {
	for _, tc := range []struct {
		class, team string
		valid       bool
	}{
		{"", "", true}, {"shared", "", true}, {"free", "", true}, {"plus", "", true}, {"pro", "", true},
		{"team", "team-a", true}, {"team", "", true}, {"plus", "team-a", false},
		{"", "team-a", false}, {"premium", "", false}, {"team", "a\nb", false},
	} {
		err := (Agent{ResourceClass: tc.class, TeamID: tc.team}).ValidateUserScope()
		if (err == nil) != tc.valid {
			t.Errorf("%q/%q: %v", tc.class, tc.team, err)
		}
	}
}

func TestLoadScopeEnvironment(t *testing.T) {
	t.Setenv("AGENT_RESOURCE_CLASS", "team")
	t.Setenv("AGENT_TEAM_ID", "tenant-b")
	cfg, err := LoadReader(strings.NewReader("agent:\n  resourceClass: shared\n"))
	if err != nil || cfg.Agent.ResourceClass != "team" || cfg.Agent.TeamID != "tenant-b" {
		t.Fatalf("scope env not applied: %v", err)
	}
}

// A pool name is metadata, even when it resembles another resource class.
func TestResourceClassIndependentOfPool(t *testing.T) {
	for _, class := range []string{"shared", "free", "plus", "team"} {
		t.Run(class, func(t *testing.T) {
			t.Setenv("AGENT_RESOURCE_CLASS", class)
			t.Setenv("AGENT_POOL", "jp-free")
			team := ""
			if class == "team" {
				team = "tenant-a"
			}
			t.Setenv("AGENT_TEAM_ID", team)
			cfg, err := LoadReader(strings.NewReader("agent:\n  resourceClass: free\n  pool: arbitrary-pool\n"))
			if err != nil {
				t.Fatal(err)
			}
			if cfg.Agent.ResourceClass != class || cfg.Agent.Pool != "arbitrary-pool" {
				t.Fatalf("resource class must be selected independently of pool: %+v", cfg.Agent)
			}
		})
	}
}
