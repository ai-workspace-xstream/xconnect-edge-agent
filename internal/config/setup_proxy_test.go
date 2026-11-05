package config

import (
	"os"
	"strings"
	"testing"
)

func TestSetupProxyDelegatesHostOperationsToPlaybooks(t *testing.T) {
	script, err := os.ReadFile("../../scripts/setup-proxy.sh")
	if err != nil {
		t.Fatal(err)
	}
	content := string(script)
	for _, required := range []string{
		"deploy|standalone|upgrade|print-arch",
		"AGENT_PLAYBOOKS_REF",
		"AGENT_PROXY_OPERATION=\"$operation\"",
		"ansible-playbook",
		"AGENT_CONTROLLER_URL",
		"BILLING_SERVICE_BASE_URL",
	} {
		if !strings.Contains(content, required) {
			t.Fatalf("missing Playbooks installer contract: %s", required)
		}
	}
	for _, removed := range []string{
		"install_xray_from_maintained_fork()",
		"install_prebuilt_runtime_bundle()",
		"write_caddy_config()",
		"apply_low_latency_tuning()",
		"AGENT_BINARY_SHA256",
		"sha256sum",
	} {
		if strings.Contains(content, removed) {
			t.Fatalf("legacy shell installer implementation remains: %s", removed)
		}
	}
}
