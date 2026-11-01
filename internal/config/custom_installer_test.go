package config

import (
	"gopkg.in/yaml.v3"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

func TestInstallerPersistsCustomAudience(t *testing.T) {
	script, err := os.ReadFile("../../scripts/setup-proxy.sh")
	if err != nil {
		t.Fatal(err)
	}
	block := strings.Split(strings.Split(string(script), "<<'PY_CONFIG'\n")[1], "\nPY_CONFIG")[0]
	for _, raw := range []string{"alice@example.com", `["alice@example.com","bob@example.com"]`} {
		path := filepath.Join(t.TempDir(), "agent.yaml")
		if err := os.WriteFile(path, []byte("agent:\n  resourceClass: custom\n  allowedEmails:\n    - stale@example.com\n  id: node\n"), 0600); err != nil {
			t.Fatal(err)
		}
		program := strings.ReplaceAll(block, "/etc/agent/account-agent.yaml", path)
		cmd := exec.Command("python3", "-c", program)
		cmd.Env = append(os.Environ(), "AGENT_PROXY_DOMAIN=node", "AUTH_URL=", "INTERNAL_SERVICE_TOKEN=", "AGENT_RESOURCE_CLASS=custom", "AGENT_CUSTOM_EMAILS="+raw)
		if output, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("installer: %v %s", err, output)
		}
		// Simulate a service restart without the installer's shell environment.
		var cfg Config
		content, _ := os.ReadFile(path)
		if err := yaml.Unmarshal(content, &cfg); err != nil {
			t.Fatal(err)
		}
		if len(cfg.Agent.AllowedEmails) == 0 || cfg.Agent.AllowedEmails[0] != "alice@example.com" || strings.Contains(string(content), "stale@example.com") {
			t.Fatalf("persisted config did not replace the audience: %s", content)
		}
	}
}
