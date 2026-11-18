package agentmode

import (
	"encoding/json"
	"io"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/ai-workspace-xstream/xconnect-edge-agent/internal/agentproto"
	"github.com/ai-workspace-xstream/xconnect-edge-agent/internal/config"
)

// Read only typed, bounded public telemetry. Never read WG/config/key state.
func readOverlayStatus(agent config.Agent) *agentproto.OverlayStatus {
	if !filepath.IsAbs(agent.OverlayStatusPath) {
		return nil
	}
	info, e := os.Lstat(agent.OverlayStatusPath)
	if e != nil || !info.Mode().IsRegular() || info.Size() > 65536 {
		return nil
	}
	file, e := os.Open(agent.OverlayStatusPath)
	if e != nil {
		return nil
	}
	defer file.Close()
	raw, e := io.ReadAll(io.LimitReader(file, 65537))
	if e != nil || len(raw) > 65536 {
		return nil
	}
	var status agentproto.OverlayStatus
	if e = json.Unmarshal(raw, &status); e != nil {
		return nil
	}
	now := time.Now()
	if status.DeviceID != firstNonEmpty(agent.NodeID, agent.ID) || status.NetworkID != agent.NetworkID || status.Role != agent.EffectiveRole() || !status.ExpiresAt.After(now) || status.UpdatedAt.After(now.Add(5*time.Second)) || now.Sub(status.UpdatedAt) > 10*time.Second || len(status.Paths) > 256 {
		return nil
	}
	for _, path := range status.Paths {
		if path.DeviceID == "" || !validOverlayPath(path.Path) {
			return nil
		}
	}
	return &status
}

func validOverlayPath(path string) bool {
	switch path {
	case "direct-lan", "relay", "vless-xhttp", "wg-udp":
		return true
	default:
		return false
	}
}

func resolveGatewayTelemetryPath(agent config.Agent) string {
	if path := strings.TrimSpace(agent.GatewayTelemetryPath); path != "" {
		return path
	}
	if agent.EffectiveRole() == config.RoleGateway && strings.TrimSpace(agent.OverlayStatusPath) != "" {
		return filepath.Join(filepath.Dir(agent.OverlayStatusPath), "gateway-telemetry.json")
	}
	return ""
}

// readGatewayTelemetry reads bounded, non-secret gateway telemetry from disk.
func readGatewayTelemetry(agent config.Agent) *agentproto.GatewayTelemetry {
	path := resolveGatewayTelemetryPath(agent)
	if !filepath.IsAbs(path) {
		return nil
	}
	info, err := os.Lstat(path)
	if err != nil || !info.Mode().IsRegular() || info.Size() > 262144 {
		return nil
	}
	file, err := os.Open(path)
	if err != nil {
		return nil
	}
	defer file.Close()
	raw, err := io.ReadAll(io.LimitReader(file, 262145))
	if err != nil || len(raw) > 262144 {
		return nil
	}
	var telemetry agentproto.GatewayTelemetry
	if err := json.Unmarshal(raw, &telemetry); err != nil {
		return nil
	}
	now := time.Now()
	if telemetry.GatewayID != firstNonEmpty(agent.NodeID, agent.ID) ||
		telemetry.NetworkID != agent.NetworkID ||
		!telemetry.ExpiresAt.After(now) ||
		telemetry.SampledAt.After(now.Add(5*time.Second)) ||
		len(telemetry.Peers) > 512 ||
		len(telemetry.TopApps) > 64 {
		return nil
	}
	return &telemetry
}
