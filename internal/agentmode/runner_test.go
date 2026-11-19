package agentmode

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/ai-workspace-xstream/xconnect-edge-agent/internal/agentproto"
	"github.com/ai-workspace-xstream/xconnect-edge-agent/internal/config"
	"github.com/ai-workspace-xstream/xconnect-edge-agent/internal/xrayconfig"
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
	if report.Xray.Region != "ph-mnl" || report.Xray.Pool != "ph" || report.Xray.Provider != "surfercloud" || report.Xray.Product != "ulighthost" {
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

func TestResolveRoutingPoolFromResourceClassAndPool(t *testing.T) {
	cases := []struct {
		name  string
		agent config.Agent
		want  string
	}{
		{name: "free", agent: config.Agent{ResourceClass: "free", Pool: "hk-main"}, want: xrayconfig.RoutingPoolMedia},
		{name: "shared", agent: config.Agent{ResourceClass: "shared", Pool: "hk-main"}, want: xrayconfig.RoutingPoolMedia},
		{name: "plus", agent: config.Agent{ResourceClass: "plus", Pool: "hk-main"}, want: xrayconfig.RoutingPoolMedia},
		{name: "pro", agent: config.Agent{ResourceClass: "pro", Pool: "jp-main"}, want: xrayconfig.RoutingPoolProEC2},
		{name: "custom", agent: config.Agent{ResourceClass: "custom"}, want: xrayconfig.RoutingPoolCustomDedicated},
		{name: "team", agent: config.Agent{ResourceClass: "team"}, want: xrayconfig.RoutingPoolCustomDedicated},
		{name: "fallback to pool", agent: config.Agent{Pool: "Pro-EC2-Pool"}, want: "Pro-EC2-Pool"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := resolveRoutingPool(tc.agent); got != tc.want {
				t.Fatalf("resolveRoutingPool() = %q, want %q", got, tc.want)
			}
		})
	}
}

func TestHTTPClientSourceReturnsControllerRoutingPolicy(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/api/agent-server/v1/users" {
			t.Fatalf("unexpected path: %s", r.URL.Path)
		}
		_ = json.NewEncoder(w).Encode(agentproto.ClientListResponse{
			Clients: []xrayconfig.Client{{ID: "uuid-1", Email: "u1@example.com", RoutingPool: xrayconfig.RoutingPoolProEC2}},
			Total:   1,
			RoutingPolicy: &agentproto.RoutingPolicy{
				Revision:         "rp-99",
				PoolTag:          xrayconfig.RoutingPoolProEC2,
				PublicResource:   true,
				SubPoolTags:      []string{"pool-pro-ai-dev", "pool-pro-unclassified"},
				MediaOnProAction: "block",
			},
		})
	}))
	defer server.Close()

	client, err := NewClient(server.URL, "token", ClientOptions{Timeout: time.Second})
	if err != nil {
		t.Fatalf("new client: %v", err)
	}
	source := NewHTTPClientSource(client, newSyncTracker())
	clients, policy, err := source.ListClientsWithPolicy(context.Background())
	if err != nil {
		t.Fatalf("ListClientsWithPolicy: %v", err)
	}
	if len(clients) != 1 || clients[0].RoutingPool != xrayconfig.RoutingPoolProEC2 {
		t.Fatalf("unexpected clients: %#v", clients)
	}
	if policy == nil || policy.PoolTag != xrayconfig.RoutingPoolProEC2 || policy.Revision != "rp-99" || !policy.PublicResource {
		t.Fatalf("unexpected routing policy: %#v", policy)
	}
}
