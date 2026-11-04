package config

import (
	"errors"
	"strings"
	"testing"
)

func TestVersionedConfigurationFailsClosed(t *testing.T) {
	for _, tc := range []struct{ name, input string }{
		{"missing version", "virtualNodes: {enabled: true}"},
		{"future version", "configVersion: 3"},
		{"quoted version", "configVersion: '2'"},
		{"empty list", "configVersion: 2\nvirtualNodes: {enabled: true}"},
		{"quoted bool", "configVersion: 2\nvirtualNodes: {enabled: 'false'}"},
		{"unknown field", "configVersion: 2\nvirtualNodez: {}"},
		{"duplicate field", "configVersion: 2\nmode: agent\nmode: agent"},
		{"multiple documents", "configVersion: 2\n---\nmode: agent"},
		{"alias", "configVersion: 2\nagent: &a {}\nxray: *a"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if _, err := LoadReader(strings.NewReader(tc.input)); err == nil {
				t.Fatal("unsafe configuration accepted")
			}
		})
	}
}
func TestDisabledVirtualNodesNeverReadsChildren(t *testing.T) {
	cfg, err := LoadReader(strings.NewReader("configVersion: 2\nmode: agent\nvirtualNodes:\n  enabled: false\n  nodes:\n    - configFile: /does-not-exist\n      runtimeFile: /does-not-exist\nagent:\n  resourceClass: custom\n  customAllowFree: true\n  allowedEmails: [user@example.invalid]\n"))
	if err != nil {
		t.Fatal(err)
	}
	if !cfg.Agent.CustomAllowFree {
		t.Fatal("custom Free opt-in lost")
	}
}
func TestVirtualNodeRuntimeNotAdvertisedOrStarted(t *testing.T) {
	if strings.Contains(Capabilities(), `"virtualNodeRuntime":true`) {
		t.Fatal("unsupported runtime advertised")
	}
	_, err := LoadReader(strings.NewReader("configVersion: 2\nvirtualNodes:\n  enabled: true\n  nodes: [{configFile: missing, runtimeFile: missing}]"))
	if !errors.Is(err, ErrVirtualNodesNotReady) {
		t.Fatalf("want runtime gate, got %v", err)
	}
}
func TestVirtualNodesRejectsEvenEmptyBusinessEnvironment(t *testing.T) {
	t.Setenv("AGENT_CUSTOM_EMAILS", "")
	_, err := LoadReader(strings.NewReader("configVersion: 2\nvirtualNodes: {enabled: true}"))
	if err == nil || !strings.Contains(err.Error(), "AGENT_CUSTOM_EMAILS") {
		t.Fatalf("want named environment conflict, got %v", err)
	}
}
