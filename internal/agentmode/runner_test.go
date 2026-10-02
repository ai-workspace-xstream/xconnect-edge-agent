package agentmode

import (
	"encoding/json"
	"strings"
	"testing"
	"time"

	"github.com/ai-workspace-xstream/xconnect-edge-agent/internal/config"
)

func TestBuildStatusReportIncludesPoolMetadata(t *testing.T) {
	loaded, err := config.LoadReader(strings.NewReader(`
agent:
  id: ph-surfercloud-01
  nodeId: ph-surfercloud-01
  networkId: net-uat
  region: ph-mnl
  pool: ph
  entryPoint: ph.entry.example
  openToUsers: false
  provider: surfercloud
  product: ulighthost
`))
	if err != nil {
		t.Fatal(err)
	}

	report := buildStatusReport(loaded.Agent, trackerSnapshot{}, time.Minute)
	if report.HeartbeatAt.IsZero() || report.HeartbeatAt.Location() != time.UTC {
		t.Fatalf("heartbeatAt = %v, want current UTC time", report.HeartbeatAt)
	}
	if report.Xray.Pool != "ph" || report.Xray.Provider != "surfercloud" || report.Xray.Product != "ulighthost" {
		t.Fatalf("status metadata = %#v", report.Xray)
	}
	if report.Xray.EntryPoint != "ph.entry.example" || report.Xray.OpenToUsers == nil || *report.Xray.OpenToUsers {
		t.Fatalf("regional entry metadata = %#v", report.Xray)
	}
	if report.Xray.NetworkID != "net-uat" {
		t.Fatalf("network id = %q, want net-uat", report.Xray.NetworkID)
	}
	payload, err := json.Marshal(report)
	if err != nil {
		t.Fatal(err)
	}
	for _, field := range []string{`"pool":"ph"`, `"provider":"surfercloud"`, `"product":"ulighthost"`, `"heartbeatAt":`} {
		if !strings.Contains(string(payload), field) {
			t.Fatalf("status payload missing %s: %s", field, payload)
		}
	}
}

func TestOnlyAgentProxyOwnsLegacyXraySynchronizer(t *testing.T) {
	for role, want := range map[string]bool{
		config.RoleAgentProxy: true,
		config.RoleGateway:    false,
		config.RoleOne:        false,
	} {
		if got := agentOwnsXraySync(config.Agent{Role: role}); got != want {
			t.Errorf("agentOwnsXraySync(%q) = %t, want %t", role, got, want)
		}
	}
}

func TestBuildStatusReportUsesConfiguredDomainAsEntry(t *testing.T) {
	report := buildStatusReport(config.Agent{ID: "runtime-1", Domain: "custom.entry.example"}, trackerSnapshot{}, time.Minute)
	if report.Xray.EntryPoint != "custom.entry.example" || report.Xray.OpenToUsers != nil {
		t.Fatalf("legacy domain report = %#v", report.Xray)
	}
}
