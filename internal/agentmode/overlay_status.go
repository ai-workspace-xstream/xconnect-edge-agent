package agentmode

import (
	"encoding/json"
	"github.com/ai-workspace-xstream/xconnect-edge-agent/internal/agentproto"
	"github.com/ai-workspace-xstream/xconnect-edge-agent/internal/config"
	"io"
	"os"
	"path/filepath"
	"time"
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
		if path.DeviceID == "" || (path.Path != "direct-lan" && path.Path != "relay") {
			return nil
		}
	}
	return &status
}
