package agentmode

import (
	"encoding/json"
	"github.com/ai-workspace-xstream/xconnect-edge-agent/internal/agentproto"
	"github.com/ai-workspace-xstream/xconnect-edge-agent/internal/config"
	"os"
	"path/filepath"
	"testing"
	"time"
)

func TestOverlayTelemetryIsBoundedBoundAndReadOnly(t *testing.T) {
	path := filepath.Join(t.TempDir(), "overlay-status.json")
	agent := config.Agent{Role: config.RoleOne, ID: "one-a", NodeID: "one-a", NetworkID: "net", OverlayStatusPath: path}
	status := agentproto.OverlayStatus{DeviceID: "one-a", NetworkID: "net", Role: "one", Capabilities: []string{"lan-udp-v1", "gateway-relay-v1"}, Healthy: true, UpdatedAt: time.Now(), ExpiresAt: time.Now().Add(time.Minute), Paths: []agentproto.OverlayPath{{DeviceID: "one-b", Path: "direct-lan"}, {DeviceID: "one-c", Path: "relay"}}}
	write := func() {
		raw, _ := json.Marshal(status)
		if e := os.WriteFile(path, raw, 0600); e != nil {
			t.Fatal(e)
		}
	}
	write()
	report := buildStatusReport(agent, trackerSnapshot{}, time.Minute)
	if report.Overlay == nil || !report.Healthy || len(report.Overlay.Paths) != 2 {
		t.Fatal("valid multi-peer report missing")
	}
	status.NetworkID = "other"
	write()
	if readOverlayStatus(agent) != nil {
		t.Fatal("cross-network telemetry accepted")
	}
	status.NetworkID = "net"
	status.UpdatedAt = time.Now().Add(-time.Minute)
	write()
	if readOverlayStatus(agent) != nil {
		t.Fatal("stale telemetry accepted")
	}
}
