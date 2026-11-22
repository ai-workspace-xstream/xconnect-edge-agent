package agentmode

import (
	"bytes"
	"context"
	"crypto/ed25519"
	"crypto/rand"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"reflect"
	"strings"
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

func makeSignedGatewayV2TestFixture(t *testing.T, now time.Time) (gatewaySignedConfig, ed25519.PrivateKey, []gatewaySigningKey) {
	t.Helper()
	pub, priv, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	keys := []gatewaySigningKey{{
		KeyID:     "key-gw-v2",
		Algorithm: "Ed25519",
		PublicKey: base64.StdEncoding.EncodeToString(pub),
		Status:    "current",
		NotBefore: now.Add(-time.Hour),
	}}
	cfg := gatewaySignedConfig{
		SchemaVersion:  2,
		Role:           "gateway",
		ConfigID:       "cfg-edge-v2",
		NetworkID:      "net_test",
		GatewayID:      "gw-edge-test",
		Generation:     9,
		IssuedAt:       now,
		ExpiresAt:      now.Add(time.Hour),
		ConnectionMode: "wg_vless_l3",
		InterfaceName:  "xconzero0",
		Address:        "10.77.0.1/32",
		ListenPort:     51820,
		MTU:            1420,
		Peers: []gatewayPeer{
			{DeviceID: "mac", WireGuardPublicKey: base64.StdEncoding.EncodeToString(make([]byte, 32)), WireGuardAddress: "10.77.0.2/32", AllowedIPs: "10.77.0.2/32"},
			{DeviceID: "bot", WireGuardPublicKey: base64.StdEncoding.EncodeToString(make([]byte, 32)), WireGuardAddress: "10.77.0.3/32", AllowedIPs: "10.77.0.3/32"},
		},
		Transport: gatewayTransport{
			Kind:       "vless-xhttp",
			ServerName: "tw.svc.plus",
			Port:       443,
			AuthID:     "11111111-1111-1111-1111-111111111111",
			Path:       "/xconnect",
			Mode:       "auto",
			Host:       "tw.svc.plus",
		},
		Mesh: &gatewayRelaySpec{
			Peers: []gatewayRelayPeer{
				{DeviceID: "mac", PublicKey: "relay-pub-mac", AllowedPeers: []string{"bot"}},
			},
		},
		Policy: &gatewayPolicyArtifact{
			SchemaVersion:   1,
			CompilerVersion: "xconnect-policy-v1",
			NetworkID:       "net_test",
			Revision:        9,
			DefaultAction:   "deny",
			ProtectedFlows:  []string{"control:controller-session"},
			Rules: []gatewayPolicyRule{
				{
					WholeDevice:        true,
					ID:                 "allow-mac-bot",
					Action:             "allow",
					SourceDevices:      []string{"mac"},
					DestinationDevices: []string{"bot"},
					Protocols:          []string{"icmp", "tcp"},
					Ports:              []int{22, 8080},
				},
				{
					ID:                 "deny-bot-mac-udp",
					Action:             "deny",
					SourceDevices:      []string{"bot"},
					DestinationDevices: []string{"mac"},
					Protocols:          []string{"udp"},
					Ports:              []int{5353},
				},
			},
		},
		Signature: gatewaySignature{Algorithm: "Ed25519", KeyID: "key-gw-v2"},
	}
	payload, err := cfg.signingBytes()
	if err != nil {
		t.Fatal(err)
	}
	cfg.Signature.Value = base64.StdEncoding.EncodeToString(ed25519.Sign(priv, payload))
	return cfg, priv, keys
}

func TestGatewayControlSyncerV2PolicySyncAcceptHeaderAndSignedConfigFile(t *testing.T) {
	now := time.Now().UTC().Truncate(time.Second)
	cfg, _, keys := makeSignedGatewayV2TestFixture(t, now)

	stateDir := t.TempDir()
	state := gatewayState{
		SchemaVersion:       1,
		GatewayID:           "gw-edge-test",
		NetworkID:           "net_test",
		Credential:          gatewayDeviceCredential{Credential: "gw-cred-secret"},
		SigningKeys:         keys,
		EnrollmentToken:     "",
		EnrollmentExpiresAt: time.Time{},
	}
	stateRaw, _ := json.MarshalIndent(state, "", "  ")
	if err := os.WriteFile(filepath.Join(stateDir, "state.json"), stateRaw, 0600); err != nil {
		t.Fatal(err)
	}

	acceptByPath := map[string]string{}
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		acceptByPath[r.URL.Path] = r.Header.Get("Accept")
		switch r.URL.Path {
		case "/api/overlay/v1/device/session":
			_ = json.NewEncoder(w).Encode(gatewaySessionResponse{
				ClientNonce:     "nonce-1",
				EnrollmentToken: "renewed-enrollment-token",
				TokenType:       "Bearer",
				IssuedAt:        now,
				ExpiresAt:       now.Add(time.Hour),
				DeviceID:        "gw-edge-test",
				NetworkID:       "net_test",
				SigningKeys:     keys,
			})
		case "/api/overlay/v1/gateway/signed-config":
			w.Header().Set("Content-Type", gatewaySignedConfigV2MediaType)
			_ = json.NewEncoder(w).Encode(cfg)
		case "/api/overlay/v1/enrollment/signed-config/9/ack":
			_ = json.NewEncoder(w).Encode(map[string]any{"acked": true})
		default:
			http.NotFound(w, r)
		}
	}))
	defer server.Close()

	var logBuf bytes.Buffer
	logger := slog.New(slog.NewJSONHandler(&logBuf, nil))

	appliedConfigCopy := filepath.Join(stateDir, "applied-copy.json")
	agentCfg := config.Agent{
		Role:          "gateway",
		ID:            "gw-edge-test",
		NodeID:        "gw-edge-test",
		NetworkID:     "net_test",
		ControllerURL: server.URL,
		Gateway: config.ControlGateway{
			Enabled:      true,
			StateDir:     stateDir,
			ApplyCommand: []string{"cp", "{{config}}", appliedConfigCopy},
		},
	}

	tracker := newSyncTracker()
	syncer, err := newGatewayControlSyncer(agentCfg, 5*time.Second, tracker, logger)
	if err != nil {
		t.Fatal(err)
	}

	if err := syncer.SyncOnce(context.Background()); err != nil {
		t.Fatalf("SyncOnce v2 failed: %v", err)
	}

	const wantConfigAccept = gatewaySignedConfigV2MediaType + ", application/json"
	if got := acceptByPath["/api/overlay/v1/gateway/signed-config"]; got != wantConfigAccept {
		t.Fatalf("signed-config Accept header = %q, want %q", got, wantConfigAccept)
	}
	if got := acceptByPath["/api/overlay/v1/device/session"]; got != "application/json" {
		t.Fatalf("device session Accept header = %q, want application/json", got)
	}
	if got := acceptByPath["/api/overlay/v1/enrollment/signed-config/9/ack"]; got != "application/json" {
		t.Fatalf("ack Accept header = %q, want application/json", got)
	}

	rawWritten, err := os.ReadFile(filepath.Join(stateDir, "runtime", "signed-config.json"))
	if err != nil {
		t.Fatalf("read runtime/signed-config.json: %v", err)
	}
	var persisted gatewaySignedConfig
	if err := json.Unmarshal(rawWritten, &persisted); err != nil {
		t.Fatalf("unmarshal runtime/signed-config.json: %v", err)
	}
	if persisted.SchemaVersion != 2 || persisted.Policy == nil {
		t.Fatalf("expected persisted v2 config with policy, got schema=%d policy=%v", persisted.SchemaVersion, persisted.Policy)
	}
	if !reflect.DeepEqual(persisted.Policy, cfg.Policy) {
		t.Fatalf("persisted policy mismatch:\ngot:  %#v\nwant: %#v", persisted.Policy, cfg.Policy)
	}
	if err := persisted.Verify(keys, now); err != nil {
		t.Fatalf("persisted signed-config.json failed verification: %v", err)
	}

	logOutput := logBuf.String()
	for _, wantLogField := range []string{`"policy_revision":9`, `"policy_default":"deny"`, `"policy_rules":2`} {
		if !strings.Contains(logOutput, wantLogField) {
			t.Fatalf("expected sync log to contain %s, got: %s", wantLogField, logOutput)
		}
	}
}

func TestGatewaySignedConfigV2VerificationParityAndTampering(t *testing.T) {
	now := time.Now().UTC().Truncate(time.Second)
	base, priv, keys := makeSignedGatewayV2TestFixture(t, now)

	// Verify exact byte-for-byte JSON signingBytes parity with XConnect-Gateway v0.1.14 contract.
	expectedV2Payload, err := json.Marshal(struct {
		SchemaVersion  int                    `json:"schema_version"`
		Role           string                 `json:"role"`
		ConfigID       string                 `json:"config_id"`
		NetworkID      string                 `json:"network_id"`
		GatewayID      string                 `json:"gateway_id"`
		Generation     uint64                 `json:"generation"`
		IssuedAt       time.Time              `json:"issued_at"`
		ExpiresAt      time.Time              `json:"expires_at"`
		ConnectionMode string                 `json:"connection_mode,omitempty"`
		InterfaceName  string                 `json:"interface_name"`
		Address        string                 `json:"address"`
		ListenPort     int                    `json:"listen_port"`
		MTU            int                    `json:"mtu"`
		Peers          []gatewayPeer          `json:"peers"`
		Transport      gatewayTransport       `json:"transport"`
		Mesh           *gatewayRelaySpec      `json:"mesh,omitempty"`
		Policy         *gatewayPolicyArtifact `json:"policy,omitempty"`
	}{
		base.SchemaVersion, base.Role, base.ConfigID, base.NetworkID, base.GatewayID, base.Generation,
		base.IssuedAt, base.ExpiresAt, strings.TrimSpace(base.ConnectionMode), base.InterfaceName,
		base.Address, base.ListenPort, base.MTU, base.Peers, base.Transport, base.Mesh, base.Policy,
	})
	if err != nil {
		t.Fatal(err)
	}
	gotV2Payload, err := base.signingBytes()
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(gotV2Payload, expectedV2Payload) {
		t.Fatalf("v2 signingBytes mismatch:\ngot:  %s\nwant: %s", gotV2Payload, expectedV2Payload)
	}

	if err := base.Verify(keys, now); err != nil {
		t.Fatalf("valid v2 config failed verification: %v", err)
	}

	// SchemaVersion 2 with nil Policy is valid when signed.
	v2NoPolicy := base
	v2NoPolicy.Policy = nil
	pNoPolicy, err := v2NoPolicy.signingBytes()
	if err != nil {
		t.Fatal(err)
	}
	v2NoPolicy.Signature.Value = base64.StdEncoding.EncodeToString(ed25519.Sign(priv, pNoPolicy))
	if err := v2NoPolicy.Verify(keys, now); err != nil {
		t.Fatalf("valid v2 config without policy failed verification: %v", err)
	}

	// SchemaVersion 2 with DefaultAction="allow" is valid when signed.
	v2Allow := base
	allowPolicy := *base.Policy
	allowPolicy.DefaultAction = "allow"
	v2Allow.Policy = &allowPolicy
	pAllow, err := v2Allow.signingBytes()
	if err != nil {
		t.Fatal(err)
	}
	v2Allow.Signature.Value = base64.StdEncoding.EncodeToString(ed25519.Sign(priv, pAllow))
	if err := v2Allow.Verify(keys, now); err != nil {
		t.Fatalf("valid v2 config with default_action=allow failed verification: %v", err)
	}

	// Tampering with any single policy field must fail Ed25519 verification.
	tamperCases := []struct {
		name   string
		mutate func(*gatewayPolicyArtifact)
	}{
		{name: "schema_version", mutate: func(p *gatewayPolicyArtifact) { p.SchemaVersion = 2 }},
		{name: "compiler_version", mutate: func(p *gatewayPolicyArtifact) { p.CompilerVersion = "xconnect-policy-v2" }},
		{name: "revision", mutate: func(p *gatewayPolicyArtifact) { p.Revision++ }},
		{name: "default_action", mutate: func(p *gatewayPolicyArtifact) { p.DefaultAction = "allow" }},
		{name: "protected_flows", mutate: func(p *gatewayPolicyArtifact) { p.ProtectedFlows = []string{"control:tampered"} }},
		{name: "rule_whole_device", mutate: func(p *gatewayPolicyArtifact) { p.Rules[0].WholeDevice = false }},
		{name: "rule_id", mutate: func(p *gatewayPolicyArtifact) { p.Rules[0].ID = "allow-other" }},
		{name: "rule_action", mutate: func(p *gatewayPolicyArtifact) { p.Rules[0].Action = "deny" }},
		{name: "rule_source_devices", mutate: func(p *gatewayPolicyArtifact) { p.Rules[0].SourceDevices = []string{"eve"} }},
		{name: "rule_destination_devices", mutate: func(p *gatewayPolicyArtifact) { p.Rules[0].DestinationDevices = []string{"eve"} }},
		{name: "rule_protocols", mutate: func(p *gatewayPolicyArtifact) { p.Rules[0].Protocols = []string{"tcp"} }},
		{name: "rule_ports", mutate: func(p *gatewayPolicyArtifact) { p.Rules[0].Ports = []int{22, 9090} }},
	}
	for _, tc := range tamperCases {
		t.Run("tamper_"+tc.name, func(t *testing.T) {
			tampered := base
			pCopy := *base.Policy
			pCopy.ProtectedFlows = append([]string(nil), base.Policy.ProtectedFlows...)
			pCopy.Rules = make([]gatewayPolicyRule, len(base.Policy.Rules))
			for i, r := range base.Policy.Rules {
				r.SourceDevices = append([]string(nil), r.SourceDevices...)
				r.DestinationDevices = append([]string(nil), r.DestinationDevices...)
				r.Protocols = append([]string(nil), r.Protocols...)
				r.Ports = append([]int(nil), r.Ports...)
				pCopy.Rules[i] = r
			}
			tc.mutate(&pCopy)
			tampered.Policy = &pCopy
			if err := tampered.Verify(keys, now); err == nil {
				t.Fatalf("expected tampered policy (%s) to fail Verify", tc.name)
			}
		})
	}

	// Reject SchemaVersion: 1 with non-nil Policy (even if signed).
	v1WithPolicy := base
	v1WithPolicy.SchemaVersion = 1
	if strings.Contains(string(func() []byte { b, _ := v1WithPolicy.signingBytes(); return b }()), `"policy"`) {
		t.Fatal("v1 signingBytes must never include policy field")
	}
	if err := v1WithPolicy.Verify(keys, now); err == nil {
		t.Fatal("expected SchemaVersion 1 with non-nil Policy to fail Verify")
	}

	// Reject SchemaVersion: 0 with non-nil Policy.
	v0WithPolicy := base
	v0WithPolicy.SchemaVersion = 0
	if err := v0WithPolicy.Verify(keys, now); err == nil {
		t.Fatal("expected SchemaVersion 0 with non-nil Policy to fail Verify")
	}

	// Reject unsupported SchemaVersion.
	v3 := base
	v3.SchemaVersion = 3
	if err := v3.Verify(keys, now); err == nil {
		t.Fatal("expected unsupported SchemaVersion 3 to fail Verify")
	}

	// Reject SchemaVersion: 2 when Policy.NetworkID != cfg.NetworkID.
	mismatchNet := base
	pMismatch := *base.Policy
	pMismatch.NetworkID = "other_net"
	mismatchNet.Policy = &pMismatch
	pBytes, _ := mismatchNet.signingBytes()
	mismatchNet.Signature.Value = base64.StdEncoding.EncodeToString(ed25519.Sign(priv, pBytes))
	if err := mismatchNet.Verify(keys, now); err == nil {
		t.Fatal("expected Policy.NetworkID mismatch to fail Verify")
	}

	// Reject SchemaVersion: 2 when Policy.DefaultAction is invalid.
	for _, badDefault := range []string{"", "reject", "DROP"} {
		badActionCfg := base
		pBad := *base.Policy
		pBad.DefaultAction = badDefault
		badActionCfg.Policy = &pBad
		pBytes, _ := badActionCfg.signingBytes()
		badActionCfg.Signature.Value = base64.StdEncoding.EncodeToString(ed25519.Sign(priv, pBytes))
		if err := badActionCfg.Verify(keys, now); err == nil {
			t.Fatalf("expected Policy.DefaultAction=%q to fail Verify", badDefault)
		}
	}
}

func TestGatewaySignedConfigV1BackwardCompatibility(t *testing.T) {
	pub, priv, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	now := time.Now().UTC().Truncate(time.Second)
	keys := []gatewaySigningKey{{
		KeyID:     "key-gw-v1",
		Algorithm: "Ed25519",
		PublicKey: base64.StdEncoding.EncodeToString(pub),
		Status:    "current",
		NotBefore: now.Add(-time.Hour),
	}}

	// V1 config signed without connection_mode still verifies when connection_mode is present.
	legacySigned := gatewaySignedConfig{
		SchemaVersion: 1,
		Role:          "gateway",
		ConfigID:      "cfg-v1-legacy",
		NetworkID:     "net_test",
		GatewayID:     "gw-edge-test",
		Generation:    5,
		IssuedAt:      now,
		ExpiresAt:     now.Add(time.Hour),
		InterfaceName: "xconzero0",
		Address:       "10.77.0.1/32",
		ListenPort:    51820,
		MTU:           1280,
		Peers:         []gatewayPeer{{DeviceID: "mac", WireGuardPublicKey: "pub", WireGuardAddress: "10.77.0.2/32", AllowedIPs: "10.77.0.2/32"}},
		Transport:     gatewayTransport{ServerName: "tw.svc.plus", Port: 443, AuthID: "11111111-1111-1111-1111-111111111111"},
		Signature:     gatewaySignature{Algorithm: "Ed25519", KeyID: "key-gw-v1"},
	}
	legacyBytes, err := legacySigned.signingBytes()
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(legacyBytes), `"policy"`) {
		t.Fatalf("v1 signingBytes should not contain policy: %s", legacyBytes)
	}
	legacySig := base64.StdEncoding.EncodeToString(ed25519.Sign(priv, legacyBytes))

	v1WithMode := legacySigned
	v1WithMode.ConnectionMode = "wg_vless_l3"
	v1WithMode.Signature.Value = legacySig
	if err := v1WithMode.Verify(keys, now); err != nil {
		t.Fatalf("expected v1 legacy connection_mode fallback to verify: %v", err)
	}

	// SchemaVersion 2 must NOT fall back to empty connection_mode.
	v2Cfg := v1WithMode
	v2Cfg.SchemaVersion = 2
	v2Cfg.ConnectionMode = ""
	v2NoModeBytes, _ := v2Cfg.signingBytes()
	v2Cfg.ConnectionMode = "wg_vless_l3"
	v2Cfg.Signature.Value = base64.StdEncoding.EncodeToString(ed25519.Sign(priv, v2NoModeBytes))
	if err := v2Cfg.Verify(keys, now); err == nil {
		t.Fatal("expected v2 config not to use legacy empty connection_mode fallback")
	}

	// SyncOnce succeeds when controller falls back to a v1 response despite v2 Accept header.
	stateDir := t.TempDir()
	state := gatewayState{
		SchemaVersion:       1,
		GatewayID:           "gw-edge-test",
		NetworkID:           "net_test",
		Credential:          gatewayDeviceCredential{Credential: "gw-cred-secret"},
		SigningKeys:         keys,
		EnrollmentToken:     "test-enrollment-token",
		EnrollmentExpiresAt: now.Add(time.Hour),
	}
	stateRaw, _ := json.MarshalIndent(state, "", "  ")
	if err := os.WriteFile(filepath.Join(stateDir, "state.json"), stateRaw, 0600); err != nil {
		t.Fatal(err)
	}

	v1Bytes, _ := v1WithMode.signingBytes()
	v1WithMode.Signature.Value = base64.StdEncoding.EncodeToString(ed25519.Sign(priv, v1Bytes))

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/api/overlay/v1/gateway/signed-config":
			_ = json.NewEncoder(w).Encode(v1WithMode)
		case "/api/overlay/v1/enrollment/signed-config/5/ack":
			w.WriteHeader(http.StatusNoContent)
		default:
			http.NotFound(w, r)
		}
	}))
	defer server.Close()

	agentCfg := config.Agent{
		Role:          "gateway",
		ID:            "gw-edge-test",
		NodeID:        "gw-edge-test",
		NetworkID:     "net_test",
		ControllerURL: server.URL,
		Gateway: config.ControlGateway{
			Enabled:      true,
			StateDir:     stateDir,
			ApplyCommand: []string{"true"},
		},
	}
	syncer, err := newGatewayControlSyncer(agentCfg, 5*time.Second, newSyncTracker(), nil)
	if err != nil {
		t.Fatal(err)
	}
	if err := syncer.SyncOnce(context.Background()); err != nil {
		t.Fatalf("SyncOnce with v1 fallback response failed: %v", err)
	}
}
