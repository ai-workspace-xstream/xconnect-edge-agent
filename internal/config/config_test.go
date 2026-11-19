package config

import (
	"os"
	"path/filepath"
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

func TestLoadMergesAgentProxyConfDAndIgnoresGatewayFragments(t *testing.T) {
	dir := t.TempDir()
	confD := filepath.Join(dir, "conf.d")
	if err := os.MkdirAll(confD, 0o755); err != nil {
		t.Fatal(err)
	}

	mainPath := filepath.Join(dir, "account-agent.yaml")
	mainYAML := `mode: agent
agent:
  id: base-id
  controllerUrl: https://accounts.example.test
  apiToken: base-secret-token
  domain: base.example.test
`
	if err := os.WriteFile(mainPath, []byte(mainYAML), 0o600); err != nil {
		t.Fatal(err)
	}

	agentProxyYAML := `agent:
  role: agent-proxy
  id: jp-xconnect-free-01.svc.plus
  pool: jp-free
  resourceClass: shared
  pricingGroup: free
  region: jp-tyo
  entryPoint: jp-free.svc.plus
  controllerUrl: ""
  apiToken: ""
xray:
  sync:
    enabled: true
    targets:
      - name: xhttp
        outputPath: /usr/local/etc/xray/config.json
        templatePath: /usr/local/etc/xray/templates/xray.xhttp.template.json
`
	if err := os.WriteFile(filepath.Join(confD, "01-agent-proxy.yaml"), []byte(agentProxyYAML), 0o600); err != nil {
		t.Fatal(err)
	}

	gatewayYAML := `agent:
  role: gateway
  id: gateway-should-be-ignored
  pool: gw-pool
  resourceClass: pro
  controllerUrl: https://evil-or-other.example.test
  apiToken: gateway-token
xray:
  sync:
    targets:
      - name: gateway-target
        outputPath: /tmp/gateway-xray.json
`
	if err := os.WriteFile(filepath.Join(confD, "02-gateway.yaml"), []byte(gatewayYAML), 0o600); err != nil {
		t.Fatal(err)
	}

	xconnectGatewayYAML := `agent:
  role: xconnect-gateway
  id: xgw-ignored
  resourceClass: custom
`
	if err := os.WriteFile(filepath.Join(confD, "03-xconnect-gateway.yaml"), []byte(xconnectGatewayYAML), 0o600); err != nil {
		t.Fatal(err)
	}

	cfg, err := Load(mainPath)
	if err != nil {
		t.Fatalf("Load: %v", err)
	}

	if cfg.Agent.Role != RoleAgentProxy {
		t.Fatalf("Agent.Role = %q, want %q", cfg.Agent.Role, RoleAgentProxy)
	}
	if cfg.Agent.ID != "jp-xconnect-free-01.svc.plus" {
		t.Fatalf("Agent.ID = %q, want jp-xconnect-free-01.svc.plus", cfg.Agent.ID)
	}
	if cfg.Agent.Pool != "jp-free" || cfg.Agent.ResourceClass != "shared" || cfg.Agent.PricingGroup != "free" {
		t.Fatalf("unexpected pool/resourceClass/pricingGroup: %#v", cfg.Agent)
	}
	if cfg.Agent.Region != "jp-tyo" || cfg.Agent.EntryPoint != "jp-free.svc.plus" || cfg.Agent.Domain != "base.example.test" {
		t.Fatalf("unexpected region/entryPoint/domain: %#v", cfg.Agent)
	}
	if cfg.Agent.ControllerURL != "https://accounts.example.test" || cfg.Agent.APIToken != "base-secret-token" {
		t.Fatalf("base credentials overwritten: url=%q token=%q", cfg.Agent.ControllerURL, cfg.Agent.APIToken)
	}
	if len(cfg.Xray.Sync.Targets) != 1 || cfg.Xray.Sync.Targets[0].Name != "xhttp" {
		t.Fatalf("Xray.Sync.Targets = %#v, want single xhttp target", cfg.Xray.Sync.Targets)
	}
}
