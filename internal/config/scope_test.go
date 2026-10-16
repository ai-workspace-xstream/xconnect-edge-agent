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
		{"", "", true}, {"shared", "", true}, {"free", "", true}, {"plus", "", true},
		{"team", "team-a", true}, {"team", "", false}, {"plus", "team-a", false},
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
