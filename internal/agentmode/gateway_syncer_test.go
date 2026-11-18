package agentmode

import (
	"context"
	"crypto/ed25519"
	"crypto/rand"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/ai-workspace-xstream/xconnect-edge-agent/internal/agentproto"
	"github.com/ai-workspace-xstream/xconnect-edge-agent/internal/config"
)

func TestGatewayControlSyncerSyncOnceAndStatusReport(t *testing.T) {
	pub, priv, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}

	stateDir := t.TempDir()
	now := time.Now().UTC().Truncate(time.Second)

	state := gatewayState{
		SchemaVersion:       1,
		Controller:          "",
		GatewayID:           "gw-edge-test",
		NetworkID:           "net_test",
		Credential:          gatewayDeviceCredential{Credential: "gw-cred-secret"},
		SigningKeys:         []gatewaySigningKey{{KeyID: "key-gw-test", Algorithm: "Ed25519", PublicKey: base64.StdEncoding.EncodeToString(pub), Status: "current", NotBefore: now.Add(-time.Hour)}},
		EnrollmentToken:     "test-enrollment-token",
		EnrollmentExpiresAt: now.Add(time.Hour),
	}
	stateRaw, _ := json.MarshalIndent(state, "", "  ")
	if err := os.WriteFile(filepath.Join(stateDir, "state.json"), stateRaw, 0600); err != nil {
		t.Fatal(err)
	}

	cfg := gatewaySignedConfig{
		SchemaVersion:  1,
		Role:           "gateway",
		ConfigID:       "cfg-edge-test",
		NetworkID:      "net_test",
		GatewayID:      "gw-edge-test",
		Generation:     3,
		IssuedAt:       now,
		ExpiresAt:      now.Add(time.Hour),
		ConnectionMode: "wg_vless_l3",
		InterfaceName:  "xconzero0",
		Address:        "10.77.0.1/32",
		ListenPort:     51820,
		MTU:            1280,
		Peers: []gatewayPeer{
			{DeviceID: "one-user1", WireGuardPublicKey: "pub1", WireGuardAddress: "10.77.0.2/32"},
		},
		Transport: gatewayTransport{
			ServerName: "tw.svc.plus",
			Port:       443,
			AuthID:     "11111111-1111-1111-1111-111111111111",
		},
		Signature: gatewaySignature{Algorithm: "Ed25519", KeyID: "key-gw-test"},
	}
	payload, err := cfg.signingBytes()
	if err != nil {
		t.Fatal(err)
	}
	cfg.Signature.Value = base64.StdEncoding.EncodeToString(ed25519.Sign(priv, payload))

	ackReceived := false
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/api/overlay/v1/gateway/signed-config", "/api/overlay/v1/enrollment/signed-config":
			_ = json.NewEncoder(w).Encode(cfg)
		case "/api/overlay/v1/enrollment/signed-config/3/ack":
			ackReceived = true
			w.WriteHeader(http.StatusNoContent)
		default:
			http.NotFound(w, r)
		}
	}))
	defer server.Close()

	appliedFlag := filepath.Join(stateDir, "applied.flag")
	agentCfg := config.Agent{
		Role:                 "gateway",
		ID:                   "gw-edge-test",
		NodeID:               "gw-edge-test",
		NetworkID:            "net_test",
		ControllerURL:        server.URL,
		OverlayStatusPath:    filepath.Join(stateDir, "runtime", "overlay-status.json"),
		GatewayTelemetryPath: filepath.Join(stateDir, "runtime", "gateway-telemetry.json"),
		Gateway: config.ControlGateway{
			Enabled:      true,
			StateDir:     stateDir,
			ApplyCommand: []string{"sh", "-c", fmt.Sprintf("touch %s", appliedFlag)},
		},
	}

	tracker := newSyncTracker()
	syncer, err := newGatewayControlSyncer(agentCfg, 5*time.Second, tracker, nil)
	if err != nil {
		t.Fatal(err)
	}

	if err := syncer.SyncOnce(context.Background()); err != nil {
		t.Fatalf("SyncOnce failed: %v", err)
	}

	if !ackReceived {
		t.Fatal("expected ACK to be sent to controller")
	}
	if _, err := os.Stat(appliedFlag); err != nil {
		t.Fatal("apply command was not executed")
	}

	// Write mock telemetry to test buildStatusReport
	_ = os.MkdirAll(filepath.Join(stateDir, "runtime"), 0755)
	gt := agentproto.GatewayTelemetry{
		SchemaVersion:     1,
		NetworkID:         "net_test",
		GatewayID:         "gw-edge-test",
		AppliedGeneration: 3,
		WireGuardRunning:  true,
		SampledAt:         now,
		ExpiresAt:         now.Add(time.Minute),
		Peers: []agentproto.GatewayPeerTelemetry{
			{DeviceID: "one-user1", Connected: true},
		},
	}
	gtBytes, _ := json.Marshal(gt)
	_ = os.WriteFile(filepath.Join(stateDir, "runtime", "gateway-telemetry.json"), gtBytes, 0644)

	osStatus := agentproto.OverlayStatus{
		NetworkID:    "net_test",
		DeviceID:     "gw-edge-test",
		Role:         "gateway",
		Capabilities: []string{"gateway", "vless-xhttp"},
		Healthy:      true,
		UpdatedAt:    now,
		ExpiresAt:    now.Add(time.Minute),
	}
	osBytes, _ := json.Marshal(osStatus)
	_ = os.WriteFile(filepath.Join(stateDir, "runtime", "overlay-status.json"), osBytes, 0644)

	report := buildStatusReport(agentCfg, tracker.Snapshot(), time.Minute)
	if report.GatewayTelemetry == nil || report.GatewayTelemetry.AppliedGeneration != 3 {
		t.Fatalf("unexpected gateway telemetry in status report: %+v", report.GatewayTelemetry)
	}
	if report.Users != 1 {
		t.Fatalf("expected 1 connected user, got %d", report.Users)
	}
}
