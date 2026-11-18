package config

import (
	"os"
	"path/filepath"
	"testing"
)

func TestLoadAllWithoutConfD(t *testing.T) {
	dir := t.TempDir()
	basePath := filepath.Join(dir, "account-agent.yaml")
	if err := os.WriteFile(basePath, []byte(`
mode: agent
agent:
  id: ph-xconnect.svc.plus
  controllerUrl: https://accounts.svc.plus
  apiToken: test-token
`), 0o600); err != nil {
		t.Fatal(err)
	}

	configs, err := LoadAll(basePath, false)
	if err != nil {
		t.Fatalf("LoadAll failed: %v", err)
	}
	if len(configs) != 1 || configs[0].Agent.ID != "ph-xconnect.svc.plus" {
		t.Fatalf("unexpected configs: %+v", configs)
	}
}

func TestLoadAllSplitsConfDProfiles(t *testing.T) {
	dir := t.TempDir()
	basePath := filepath.Join(dir, "account-agent.yaml")
	if err := os.WriteFile(basePath, []byte(`
mode: agent
log:
  level: info
agent:
  region: PH
  controllerUrl: https://accounts.svc.plus
  apiToken: shared-token
`), 0o600); err != nil {
		t.Fatal(err)
	}

	confDir := filepath.Join(dir, "conf.d")
	if err := os.MkdirAll(confDir, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(confDir, "01-agent-proxy.yaml"), []byte(`
agent:
  id: ph-xconnect.svc.plus
  entryPoint: ph-xconnect.svc.plus
  pool: ph-main
  resourceClass: plus
`), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(confDir, "02-ph-xconnect-gateway.yaml"), []byte(`
agent:
  id: ph-xconnect-gateway-01
  role: gateway
  entryPoint: ph-xconnect-gateway-01.svc.plus
  networkId: net-ph-xconnect
  gateway:
    enabled: true
    stateDir: /var/lib/xconnect-gateway
`), 0o600); err != nil {
		t.Fatal(err)
	}

	configs, err := LoadAll(basePath, false)
	if err != nil {
		t.Fatalf("LoadAll failed: %v", err)
	}
	if len(configs) != 2 {
		t.Fatalf("expected 2 configs, got %d", len(configs))
	}
	if configs[0].Agent.ID != "ph-xconnect.svc.plus" || configs[0].Agent.EffectiveRole() != RoleAgentProxy {
		t.Fatalf("unexpected first config: %+v", configs[0].Agent)
	}
	if configs[1].Agent.ID != "ph-xconnect-gateway-01" || configs[1].Agent.EffectiveRole() != RoleGateway {
		t.Fatalf("unexpected second config: %+v", configs[1].Agent)
	}
	if configs[1].Agent.ControllerURL != "https://accounts.svc.plus" || configs[1].Agent.APIToken != "shared-token" {
		t.Fatalf("expected shared controller/token to be inherited: %+v", configs[1].Agent)
	}
}
