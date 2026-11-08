package config

import (
	"os"
	"path/filepath"
	"testing"
)

func TestLoadManagedPreservesLegacyBootstrapAndLocalExecutionSettings(t *testing.T) {
	dir := t.TempDir()
	bootstrap := filepath.Join(dir, "account-agent.yaml")
	legacy := `mode: agent
agent:
  id: jp-node
  nodeId: jp-node
  controllerUrl: https://accounts.example.test
  apiToken: local-secret
  tls:
    insecureSkipVerify: false
xray:
  sync:
    enabled: true
    targets:
      - name: xhttp
        outputPath: /etc/xray/xhttp.json
        restartCommand: [systemctl, restart, xray-xhttp]
      - name: tcp
        outputPath: /etc/xray/tcp.json
        restartCommand: [systemctl, restart, xray-tcp]
`
	if err := os.WriteFile(bootstrap, []byte(legacy), 0o600); err != nil {
		t.Fatal(err)
	}
	overlayDir := filepath.Join(dir, "account-agent.d")
	if err := os.Mkdir(overlayDir, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(overlayDir, "managed.yaml"), []byte(`agent:
  region: tokyo
xray:
  sync:
    interval: 5m
`), 0o600); err != nil {
		t.Fatal(err)
	}

	cfg, err := LoadManaged(bootstrap)
	if err != nil {
		t.Fatal(err)
	}
	if cfg.Agent.ID != "jp-node" || cfg.Agent.ControllerURL != "https://accounts.example.test" || cfg.Agent.APIToken != "local-secret" || cfg.Agent.TLS.InsecureSkipVerify {
		t.Fatalf("managed snapshot changed bootstrap fields: %+v", cfg.Agent)
	}
	if cfg.Agent.Region != "tokyo" || cfg.Xray.Sync.Interval.String() != "5m0s" {
		t.Fatalf("managed fields not applied: %+v", cfg)
	}
	if len(cfg.Xray.Sync.Targets) != 2 || cfg.Xray.Sync.Targets[0].OutputPath != "/etc/xray/xhttp.json" || cfg.Xray.Sync.Targets[1].OutputPath != "/etc/xray/tcp.json" {
		t.Fatalf("local Xray targets were not preserved: %+v", cfg.Xray.Sync.Targets)
	}
}

func TestLoadManagedRejectsUnsafeOrUnsupportedSnapshotFields(t *testing.T) {
	tests := map[string]string{
		"bootstrap token": `agent: {apiToken: attacker-token}`,
		"tls":             `agent: {tls: {insecureSkipVerify: true}}`,
		"scope":           `agent: {resourceClass: plus, allowedEmails: [all@example.test]}`,
		"command":         "xray: {sync: {targets: [{name: xhttp, restartCommand: [sh, -c, id]}]}}",
		"path":            "xray: {sync: {outputPath: /tmp/evil}}",
		"mode":            "mode: gateway",
		"unknown":         "agent: {undocumented: true}",
		"merge key":       "base: &base {region: tokyo}\nagent: {<<: *base}",
		"duplicate":       "agent: {region: tokyo, region: osaka}",
		"multi document":  "agent: {region: tokyo}\n---\nagent: {region: osaka}",
	}
	for name, snapshot := range tests {
		t.Run(name, func(t *testing.T) {
			dir := t.TempDir()
			bootstrap := filepath.Join(dir, "account-agent.yaml")
			if err := os.WriteFile(bootstrap, []byte("agent:\n  id: node\n  controllerUrl: https://accounts.example.test\n  apiToken: local-token\n"), 0o600); err != nil {
				t.Fatal(err)
			}
			overlayDir := filepath.Join(dir, "account-agent.d")
			if err := os.Mkdir(overlayDir, 0o700); err != nil {
				t.Fatal(err)
			}
			if err := os.WriteFile(filepath.Join(overlayDir, "managed.yaml"), []byte(snapshot), 0o600); err != nil {
				t.Fatal(err)
			}
			if _, err := LoadManaged(bootstrap); err == nil {
				t.Fatal("expected managed snapshot rejection")
			}
		})
	}
}

func TestLoadKeepsLegacyLoaderIndependentFromManagedSnapshot(t *testing.T) {
	dir := t.TempDir()
	bootstrap := filepath.Join(dir, "account-agent.yaml")
	if err := os.WriteFile(bootstrap, []byte("agent:\n  id: old-node\n  controllerUrl: https://accounts.example.test\n  apiToken: local-token\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := Load(bootstrap); err != nil {
		t.Fatalf("legacy loader unexpectedly requires managed snapshot: %v", err)
	}
}

func TestLoadManagedCompatibilityMatrixForPublicResourceClasses(t *testing.T) {
	for _, class := range []string{"free", "shared", "plus", "pro", "custom"} {
		t.Run(class, func(t *testing.T) {
			dir := t.TempDir()
			bootstrap := filepath.Join(dir, "account-agent.yaml")
			// Old binaries continue to read this minimal shape through Load;
			// custom retains its required static email allowlist for rollback.
			legacy := "id: node-" + class + "\nnodeId: node-" + class + "\ncontrollerUrl: https://accounts.example.test\napiToken: local-token\nresourceClass: " + class + "\n"
			if class == "custom" {
				legacy += "agent:\n  allowedEmails: [legacy@example.test]\n"
			}
			if err := os.WriteFile(bootstrap, []byte(legacy), 0o600); err != nil {
				t.Fatal(err)
			}
			if _, err := Load(bootstrap); err != nil {
				t.Fatalf("old loader rejected five-field config: %v", err)
			}
			overlayDir := filepath.Join(dir, "account-agent.d")
			if err := os.Mkdir(overlayDir, 0o700); err != nil {
				t.Fatal(err)
			}
			snapshot := "agent:\n  region: test-region\n"
			if err := os.WriteFile(filepath.Join(overlayDir, "managed.yaml"), []byte(snapshot), 0o600); err != nil {
				t.Fatal(err)
			}
			cfg, err := LoadManaged(bootstrap)
			if err != nil {
				t.Fatalf("managed loader rejected %s config: %v", class, err)
			}
			if cfg.Agent.ResourceClass != class || cfg.Agent.NodeID != "node-"+class {
				t.Fatalf("loaded config class/node mismatch: class=%q node=%q", cfg.Agent.ResourceClass, cfg.Agent.NodeID)
			}
			if cfg.Agent.Region != "test-region" {
				t.Fatalf("managed region = %q, want test-region", cfg.Agent.Region)
			}
		})
	}
}
