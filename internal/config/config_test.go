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

func TestLoadReadsLocalXrayTuning(t *testing.T) {
	cfg, err := LoadReader(strings.NewReader(`mode: agent
agent:
  controllerUrl: https://accounts.example.test
  apiToken: token
xray:
  sync:
    enabled: true
    tuning:
      logLevel: warning
      bufferSizeKB: 512
      xhttpScMaxEachPostBytes: 2097152
`))
	if err != nil {
		t.Fatal(err)
	}
	tuning := cfg.Xray.Sync.Tuning
	if tuning == nil || tuning.LogLevel != "warning" || *tuning.BufferSizeKB != 512 || *tuning.XHTTPMaxEachPostBytes != 2097152 {
		t.Fatalf("local tuning not decoded: %+v", tuning)
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

func TestLoadMinimalBootstrapConfig(t *testing.T) {
	t.Run("flat minimal 5-field bootstrap", func(t *testing.T) {
		raw := `id: jp-free-01
nodeId: jp-free-node
controllerUrl: https://accounts.svc.plus
apiToken: secret-token-xyz
resourceClass: free
`
		cfg, err := LoadReader(strings.NewReader(raw))
		if err != nil {
			t.Fatalf("unexpected load error: %v", err)
		}
		if cfg.Mode != "agent" {
			t.Errorf("expected Mode agent, got %q", cfg.Mode)
		}
		if cfg.Agent.ID != "jp-free-01" {
			t.Errorf("expected Agent.ID jp-free-01, got %q", cfg.Agent.ID)
		}
		if cfg.Agent.NodeID != "jp-free-node" {
			t.Errorf("expected Agent.NodeID jp-free-node, got %q", cfg.Agent.NodeID)
		}
		if cfg.Agent.ControllerURL != "https://accounts.svc.plus" {
			t.Errorf("expected Agent.ControllerURL https://accounts.svc.plus, got %q", cfg.Agent.ControllerURL)
		}
		if cfg.Agent.APIToken != "secret-token-xyz" {
			t.Errorf("expected Agent.APIToken secret-token-xyz, got %q", cfg.Agent.APIToken)
		}
		if cfg.Agent.ResourceClass != "free" {
			t.Errorf("expected Agent.ResourceClass free, got %q", cfg.Agent.ResourceClass)
		}
		if cfg.Agent.EffectiveRole() != RoleAgentProxy {
			t.Errorf("expected RoleAgentProxy, got %q", cfg.Agent.EffectiveRole())
		}
	})

	t.Run("nodeId defaults to id when omitted", func(t *testing.T) {
		raw := `id: ph-xconnect-agent
controllerUrl: https://accounts.svc.plus
apiToken: secret-token-xyz
resourceClass: plus
`
		cfg, err := LoadReader(strings.NewReader(raw))
		if err != nil {
			t.Fatalf("unexpected load error: %v", err)
		}
		if cfg.Agent.NodeID != "ph-xconnect-agent" {
			t.Errorf("expected NodeID to default to ID, got %q", cfg.Agent.NodeID)
		}
		if cfg.Agent.ResourceClass != "plus" {
			t.Errorf("expected plus, got %q", cfg.Agent.ResourceClass)
		}
	})

	t.Run("minimal 5-field under agent section without mode", func(t *testing.T) {
		raw := `agent:
  id: team-node-01
  nodeId: team-node-01
  controllerUrl: https://accounts.svc.plus
  apiToken: secret-token-team
  resourceClass: team
`
		cfg, err := LoadReader(strings.NewReader(raw))
		if err != nil {
			t.Fatalf("unexpected load error: %v", err)
		}
		if cfg.Mode != "agent" {
			t.Errorf("expected default mode agent, got %q", cfg.Mode)
		}
		if cfg.Agent.ResourceClass != "team" {
			t.Errorf("expected team, got %q", cfg.Agent.ResourceClass)
		}
	})
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
