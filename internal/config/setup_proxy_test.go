package config

import (
	"bytes"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

func TestSetupProxyBackfillsLegacyDynamicUsers(t *testing.T) {
	script, err := os.ReadFile("../../scripts/setup-proxy.sh")
	if err != nil {
		t.Fatal(err)
	}
	start := strings.Index(string(script), "ensure_agent_dynamic_users() {")
	if start < 0 {
		t.Fatal("missing config migration")
	}
	end := strings.Index(string(script[start:]), "\n}\n")
	if end < 0 {
		t.Fatal("missing config migration boundary")
	}
	function := string(script[start : start+end+3])
	for _, existing := range []bool{false, true} {
		t.Run(map[bool]string{false: "legacy", true: "operator-configured"}[existing], func(t *testing.T) {
			config := `agent:
  id: sg-node
  region: SG
  pool: sg-main
  entryPoint: sg.entry.example
  openToUsers: false
xray:
  sync:
    targets:
      - name: "xhttp"
        outputPath: /tmp/xhttp.json
        restartCommand: [systemctl, restart, xray.service]
`
			if existing {
				config += "        dynamicUsers:\n          enabled: false\n          server: custom:1234\n"
			}
			config += `      - name: "tcp"
        outputPath: /tmp/tcp.json
      - name: "custom"
        outputPath: /tmp/custom.json
log:
  level: debug
`
			path := filepath.Join(t.TempDir(), "agent.yaml")
			if err := os.WriteFile(path, []byte(config), 0600); err != nil {
				t.Fatal(err)
			}
			migrate := func() []byte {
				t.Helper()
				cmd := exec.Command("bash", "-c", function+"\nensure_agent_dynamic_users \"$1\"", "test", path)
				if out, err := cmd.CombinedOutput(); err != nil {
					t.Fatalf("migration: %v %s", err, out)
				}
				out, err := os.ReadFile(path)
				if err != nil {
					t.Fatal(err)
				}
				return out
			}
			first := migrate()
			if !bytes.Equal(first, migrate()) {
				t.Fatal("migration is not idempotent")
			}
			cfg, err := LoadReader(bytes.NewReader(first))
			if err != nil {
				t.Fatal(err)
			}
			if cfg.Agent.Region != "SG" || cfg.Agent.Pool != "sg-main" || cfg.Agent.OpenToUsers == nil || *cfg.Agent.OpenToUsers || cfg.Log.Level != "debug" {
				t.Fatal("operator metadata changed")
			}
			xhttp, tcp, custom := cfg.Xray.Sync.Targets[0], cfg.Xray.Sync.Targets[1], cfg.Xray.Sync.Targets[2]
			if existing {
				if xhttp.DynamicUsers.Enabled || xhttp.DynamicUsers.Server != "custom:1234" {
					t.Fatal("explicit dynamic users settings overwritten")
				}
			} else if !xhttp.DynamicUsers.Enabled || xhttp.DynamicUsers.Server != "127.0.0.1:10086" {
				t.Fatalf("xhttp missing migration: %#v", xhttp.DynamicUsers)
			}
			if !tcp.DynamicUsers.Enabled || tcp.DynamicUsers.Server != "127.0.0.1:10087" || tcp.DynamicUsers.Executable != "/usr/local/bin/xray" || custom.DynamicUsers.Enabled {
				t.Fatal("target-specific migration failed")
			}
			if len(xhttp.RestartCommand) != 3 {
				t.Fatal("restart path changed")
			}
		})
	}
}
