package agentmode

import (
	"bytes"
	"context"
	"crypto/ed25519"
	"crypto/rand"
	"crypto/tls"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"github.com/ai-workspace-xstream/xconnect-edge-agent/internal/config"
)

const defaultGatewayStateDir = "/var/lib/xconnect-gateway"

type gatewayDeviceCredential struct {
	CredentialID string    `json:"credential_id,omitempty"`
	Credential   string    `json:"credential"`
	TokenType    string    `json:"token_type,omitempty"`
	IssuedAt     time.Time `json:"issued_at,omitempty"`
	ExpiresAt    time.Time `json:"expires_at,omitempty"`
	Scope        []string  `json:"scope,omitempty"`
}

func (c *gatewayDeviceCredential) UnmarshalJSON(data []byte) error {
	trimmed := bytes.TrimSpace(data)
	if len(trimmed) > 0 && trimmed[0] == '"' {
		var raw string
		if err := json.Unmarshal(trimmed, &raw); err != nil {
			return err
		}
		c.Credential = raw
		return nil
	}
	type alias gatewayDeviceCredential
	var parsed alias
	if err := json.Unmarshal(trimmed, &parsed); err != nil {
		return err
	}
	*c = gatewayDeviceCredential(parsed)
	return nil
}

type gatewaySigningKey struct {
	KeyID     string     `json:"key_id"`
	Algorithm string     `json:"algorithm"`
	PublicKey string     `json:"public_key"`
	Status    string     `json:"status"`
	NotBefore time.Time  `json:"not_before"`
	NotAfter  *time.Time `json:"not_after,omitempty"`
}

type gatewayPeer struct {
	DeviceID           string `json:"device_id"`
	WireGuardPublicKey string `json:"wireguard_public_key"`
	WireGuardAddress   string `json:"wireguard_address"`
	AllowedIPs         string `json:"allowed_ips"`
}

type gatewayTransport struct {
	Kind         string `json:"kind,omitempty"`
	ServerName   string `json:"server_name"`
	Port         int    `json:"port"`
	AuthID       string `json:"auth_id"`
	Path         string `json:"path,omitempty"`
	Mode         string `json:"mode,omitempty"`
	Host         string `json:"host,omitempty"`
	Frontend     string `json:"frontend,omitempty"`
	ListenSocket string `json:"listen_socket,omitempty"`
}

type gatewayRelayPeer struct {
	DeviceID     string   `json:"device_id"`
	PublicKey    string   `json:"public_key"`
	AllowedPeers []string `json:"allowed_peers"`
}

type gatewayRelaySpec struct {
	Peers []gatewayRelayPeer `json:"peers"`
}

type gatewaySignature struct {
	Algorithm string `json:"algorithm"`
	KeyID     string `json:"key_id"`
	Value     string `json:"value"`
}

type gatewaySignedConfig struct {
	SchemaVersion  int               `json:"schema_version"`
	Role           string            `json:"role"`
	ConfigID       string            `json:"config_id"`
	NetworkID      string            `json:"network_id"`
	GatewayID      string            `json:"gateway_id"`
	Generation     uint64            `json:"generation"`
	IssuedAt       time.Time         `json:"issued_at"`
	ExpiresAt      time.Time         `json:"expires_at"`
	ConnectionMode string            `json:"connection_mode,omitempty"`
	InterfaceName  string            `json:"interface_name"`
	Address        string            `json:"address"`
	ListenPort     int               `json:"listen_port"`
	MTU            int               `json:"mtu"`
	Peers          []gatewayPeer     `json:"peers"`
	Transport      gatewayTransport  `json:"transport"`
	Mesh           *gatewayRelaySpec `json:"mesh,omitempty"`
	Signature      gatewaySignature  `json:"signature"`
}

func (c gatewaySignedConfig) signingBytes() ([]byte, error) {
	return json.Marshal(struct {
		SchemaVersion  int               `json:"schema_version"`
		Role           string            `json:"role"`
		ConfigID       string            `json:"config_id"`
		NetworkID      string            `json:"network_id"`
		GatewayID      string            `json:"gateway_id"`
		Generation     uint64            `json:"generation"`
		IssuedAt       time.Time         `json:"issued_at"`
		ExpiresAt      time.Time         `json:"expires_at"`
		ConnectionMode string            `json:"connection_mode,omitempty"`
		InterfaceName  string            `json:"interface_name"`
		Address        string            `json:"address"`
		ListenPort     int               `json:"listen_port"`
		MTU            int               `json:"mtu"`
		Peers          []gatewayPeer     `json:"peers"`
		Transport      gatewayTransport  `json:"transport"`
		Mesh           *gatewayRelaySpec `json:"mesh,omitempty"`
	}{
		c.SchemaVersion,
		c.Role,
		c.ConfigID,
		c.NetworkID,
		c.GatewayID,
		c.Generation,
		c.IssuedAt,
		c.ExpiresAt,
		strings.TrimSpace(c.ConnectionMode),
		c.InterfaceName,
		c.Address,
		c.ListenPort,
		c.MTU,
		c.Peers,
		c.Transport,
		c.Mesh,
	})
}

func (c gatewaySignedConfig) Verify(keys []gatewaySigningKey, now time.Time) error {
	if c.ConfigID == "" || c.NetworkID == "" || c.GatewayID == "" || c.Generation == 0 ||
		c.Signature.Algorithm != "Ed25519" || !c.ExpiresAt.After(now) ||
		(!c.IssuedAt.IsZero() && c.IssuedAt.After(now.Add(30*time.Second))) {
		return errors.New("invalid gateway signed config")
	}
	payload, err := c.signingBytes()
	if err != nil {
		return err
	}
	var legacyPayload []byte
	if strings.TrimSpace(c.ConnectionMode) != "" {
		legacy := c
		legacy.ConnectionMode = ""
		legacyPayload, _ = legacy.signingBytes()
	}
	sig, err := decodeBase64Bytes(c.Signature.Value)
	if err != nil || len(sig) != ed25519.SignatureSize {
		return errors.New("invalid gateway signature")
	}
	for _, key := range keys {
		if c.Signature.KeyID != "" && key.KeyID != "" && key.KeyID != c.Signature.KeyID {
			continue
		}
		if key.Algorithm != "" && key.Algorithm != "Ed25519" {
			continue
		}
		if key.Status != "" && key.Status != "current" && key.Status != "retiring" {
			continue
		}
		if !key.NotBefore.IsZero() && !c.IssuedAt.IsZero() && key.NotBefore.After(c.IssuedAt) {
			continue
		}
		if key.NotAfter != nil && !c.ExpiresAt.Before(*key.NotAfter) {
			continue
		}
		publicKey, decodeErr := decodeBase64Bytes(key.PublicKey)
		if decodeErr == nil && len(publicKey) == ed25519.PublicKeySize {
			if ed25519.Verify(publicKey, payload, sig) || (len(legacyPayload) > 0 && ed25519.Verify(publicKey, legacyPayload, sig)) {
				return nil
			}
		}
	}
	return errors.New("gateway config signature is not trusted")
}

func decodeBase64Bytes(value string) ([]byte, error) {
	decoded, err := base64.StdEncoding.DecodeString(strings.TrimSpace(value))
	if err != nil {
		decoded, err = base64.RawStdEncoding.DecodeString(strings.TrimSpace(value))
	}
	return decoded, err
}

type gatewayState struct {
	SchemaVersion       int                     `json:"schema_version,omitempty"`
	Controller          string                  `json:"controller,omitempty"`
	GatewayID           string                  `json:"gateway_id"`
	NetworkID           string                  `json:"network_id"`
	PrivateKey          string                  `json:"wireguard_private_key,omitempty"`
	PublicKey           string                  `json:"wireguard_public_key,omitempty"`
	Credential          gatewayDeviceCredential `json:"device_credential"`
	SigningKeys         []gatewaySigningKey     `json:"signing_keys"`
	EnrollmentToken     string                  `json:"enrollment_token,omitempty"`
	EnrollmentExpiresAt time.Time               `json:"enrollment_expires_at,omitempty"`
	AppliedConfigID     string                  `json:"applied_config_id,omitempty"`
	AppliedGeneration   uint64                  `json:"applied_generation,omitempty"`
}

type gatewaySessionResponse struct {
	ClientNonce     string              `json:"client_nonce"`
	EnrollmentToken string              `json:"enrollment_token"`
	TokenType       string              `json:"token_type"`
	IssuedAt        time.Time           `json:"issued_at"`
	ExpiresAt       time.Time           `json:"expires_at"`
	Scope           []string            `json:"scope"`
	DeviceID        string              `json:"device_id"`
	NetworkID       string              `json:"network_id"`
	SigningKeys     []gatewaySigningKey `json:"signing_keys"`
}

type gatewayControlSyncer struct {
	agent      config.Agent
	stateDir   string
	httpClient *http.Client
	tracker    *syncTracker
	logger     *slog.Logger
}

func gatewayControlSyncEnabled(agent config.Agent) bool {
	return agent.EffectiveRole() == config.RoleGateway &&
		(agent.Gateway.Enabled || strings.TrimSpace(agent.Gateway.StateDir) != "")
}

func newGatewayControlSyncer(agent config.Agent, httpTimeout time.Duration, tracker *syncTracker, logger *slog.Logger) (*gatewayControlSyncer, error) {
	stateDir := strings.TrimSpace(agent.Gateway.StateDir)
	if stateDir == "" {
		stateDir = defaultGatewayStateDir
	}
	if !filepath.IsAbs(stateDir) {
		return nil, errors.New("agent.gateway.stateDir must be an absolute path")
	}
	if httpTimeout <= 0 {
		httpTimeout = 15 * time.Second
	}
	transport := http.DefaultTransport
	if t, ok := transport.(*http.Transport); ok {
		clone := t.Clone()
		if agent.TLS.InsecureSkipVerify {
			if clone.TLSClientConfig == nil {
				clone.TLSClientConfig = &tls.Config{}
			}
			clone.TLSClientConfig.InsecureSkipVerify = true
		}
		transport = clone
	}
	if logger == nil {
		logger = slog.Default()
	}
	return &gatewayControlSyncer{
		agent:    agent,
		stateDir: stateDir,
		httpClient: &http.Client{
			Timeout:   httpTimeout,
			Transport: transport,
		},
		tracker: tracker,
		logger:  logger.With("component", "gateway-control-sync"),
	}, nil
}

func startGatewayControlSyncer(ctx context.Context, agent config.Agent, syncInterval time.Duration, httpTimeout time.Duration, tracker *syncTracker, logger *slog.Logger) (func(context.Context) error, error) {
	syncer, err := newGatewayControlSyncer(agent, httpTimeout, tracker, logger)
	if err != nil {
		return nil, err
	}
	interval := userConfigSyncInterval(syncInterval)
	runCtx, cancel := context.WithCancel(ctx)
	var wg sync.WaitGroup
	wg.Add(1)
	go func() {
		defer wg.Done()
		syncer.run(runCtx, interval)
	}()
	return func(waitCtx context.Context) error {
		cancel()
		done := make(chan struct{})
		go func() {
			wg.Wait()
			close(done)
		}()
		select {
		case <-done:
			return nil
		case <-waitCtx.Done():
			return waitCtx.Err()
		}
	}, nil
}

func (s *gatewayControlSyncer) run(ctx context.Context, interval time.Duration) {
	ticker := time.NewTicker(interval)
	defer ticker.Stop()

	step := func() {
		if err := s.SyncOnce(ctx); err != nil {
			if ctx.Err() != nil {
				return
			}
			s.logger.Warn("gateway control-plane sync failed", "err", err)
			if s.tracker != nil {
				s.tracker.MarkError(err, time.Now().UTC())
			}
		}
	}

	step()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			step()
		}
	}
}

func (s *gatewayControlSyncer) SyncOnce(ctx context.Context) error {
	state, err := s.loadState()
	if err != nil {
		return err
	}
	expectedGatewayID := firstNonEmpty(s.agent.NodeID, s.agent.ID)
	if expectedGatewayID != "" && state.GatewayID != expectedGatewayID {
		return errors.New("gateway state gateway_id mismatch")
	}
	if strings.TrimSpace(s.agent.NetworkID) != "" && state.NetworkID != strings.TrimSpace(s.agent.NetworkID) {
		return errors.New("gateway state network_id mismatch")
	}

	baseURL, err := url.Parse(strings.TrimRight(firstNonEmpty(s.agent.ControllerURL, state.Controller), "/"))
	if err != nil || baseURL.Scheme == "" || baseURL.Host == "" {
		return errors.New("invalid gateway controller URL")
	}

	now := time.Now().UTC()
	token := strings.TrimSpace(state.EnrollmentToken)
	if token == "" || !state.EnrollmentExpiresAt.After(now.Add(time.Minute)) {
		credential := strings.TrimSpace(state.Credential.Credential)
		if credential == "" {
			return errors.New("gateway device credential is required to renew enrollment session")
		}
		session, err := s.renewSession(ctx, baseURL, credential)
		if err != nil {
			return err
		}
		if session.DeviceID != "" && session.DeviceID != state.GatewayID {
			return errors.New("gateway session device_id mismatch")
		}
		if session.NetworkID != "" && session.NetworkID != state.NetworkID {
			return errors.New("gateway session network_id mismatch")
		}
		if strings.TrimSpace(session.EnrollmentToken) == "" {
			return errors.New("gateway session returned empty enrollment_token")
		}
		token = strings.TrimSpace(session.EnrollmentToken)
		state.EnrollmentToken = token
		state.EnrollmentExpiresAt = session.ExpiresAt
		if len(session.SigningKeys) > 0 {
			state.SigningKeys = session.SigningKeys
		}
		if err := s.saveState(state); err != nil {
			return err
		}
	}

	cfg, err := s.fetchSignedConfig(ctx, baseURL, token)
	if err != nil {
		return err
	}
	if cfg.GatewayID != state.GatewayID || cfg.NetworkID != state.NetworkID {
		return errors.New("gateway signed-config identity mismatch")
	}
	if err := cfg.Verify(state.SigningKeys, time.Now().UTC()); err != nil {
		return err
	}

	signedConfigPath, err := s.writeSignedConfig(cfg)
	if err != nil {
		return err
	}
	if err := s.runApplyCommand(ctx, signedConfigPath); err != nil {
		return err
	}
	if err := s.ackSignedConfig(ctx, baseURL, token, cfg); err != nil {
		return err
	}

	state.AppliedConfigID = cfg.ConfigID
	state.AppliedGeneration = cfg.Generation
	if err := s.saveState(state); err != nil {
		return err
	}

	if s.tracker != nil {
		completedAt := time.Now().UTC()
		s.tracker.UpdateFetch(len(cfg.Peers), fmt.Sprint(cfg.Generation), completedAt)
		s.tracker.MarkSuccess(completedAt)
	}
	return nil
}

func (s *gatewayControlSyncer) loadState() (gatewayState, error) {
	var state gatewayState
	statePath := filepath.Join(s.stateDir, "state.json")
	info, err := os.Lstat(statePath)
	if err != nil {
		return state, fmt.Errorf("stat gateway state: %w", err)
	}
	if !info.Mode().IsRegular() || info.Size() > 262144 {
		return state, errors.New("invalid gateway state file")
	}
	raw, err := os.ReadFile(statePath)
	if err != nil {
		return state, fmt.Errorf("read gateway state: %w", err)
	}
	if err := json.Unmarshal(raw, &state); err != nil {
		return state, fmt.Errorf("decode gateway state: %w", err)
	}
	if strings.TrimSpace(state.GatewayID) == "" || strings.TrimSpace(state.NetworkID) == "" {
		return state, errors.New("gateway state requires gateway_id and network_id")
	}
	return state, nil
}

func (s *gatewayControlSyncer) saveState(state gatewayState) error {
	if state.SchemaVersion == 0 {
		state.SchemaVersion = 1
	}
	if err := os.MkdirAll(s.stateDir, 0700); err != nil {
		return err
	}
	raw, err := json.MarshalIndent(state, "", "  ")
	if err != nil {
		return err
	}
	tmpPath := filepath.Join(s.stateDir, ".state.json.tmp")
	if err := os.WriteFile(tmpPath, raw, 0600); err != nil {
		return err
	}
	if err := os.Chmod(tmpPath, 0600); err != nil {
		return err
	}
	return os.Rename(tmpPath, filepath.Join(s.stateDir, "state.json"))
}

func (s *gatewayControlSyncer) writeSignedConfig(cfg gatewaySignedConfig) (string, error) {
	runtimeDir := filepath.Join(s.stateDir, "runtime")
	if err := os.MkdirAll(runtimeDir, 0700); err != nil {
		return "", err
	}
	raw, err := json.Marshal(cfg)
	if err != nil {
		return "", err
	}
	signedConfigPath := filepath.Join(runtimeDir, "signed-config.json")
	tmpPath := filepath.Join(runtimeDir, ".signed-config.json.tmp")
	if err := os.WriteFile(tmpPath, raw, 0600); err != nil {
		return "", err
	}
	if err := os.Chmod(tmpPath, 0600); err != nil {
		return "", err
	}
	if err := os.Rename(tmpPath, signedConfigPath); err != nil {
		return "", err
	}
	return signedConfigPath, nil
}

func (s *gatewayControlSyncer) runApplyCommand(ctx context.Context, signedConfigPath string) error {
	cmdArgs := s.agent.Gateway.ApplyCommand
	if len(cmdArgs) == 0 {
		cmdArgs = []string{"xconnect-gateway", "apply", "--state-dir", s.stateDir, "--config", signedConfigPath}
	} else {
		expanded := make([]string, len(cmdArgs))
		for i, arg := range cmdArgs {
			arg = strings.ReplaceAll(arg, "{{state_dir}}", s.stateDir)
			arg = strings.ReplaceAll(arg, "{{config}}", signedConfigPath)
			expanded[i] = arg
		}
		cmdArgs = expanded
	}
	if len(cmdArgs) == 0 || strings.TrimSpace(cmdArgs[0]) == "" {
		return errors.New("gateway apply command is empty")
	}
	cmd := exec.CommandContext(ctx, cmdArgs[0], cmdArgs[1:]...)
	cmd.Env = append(os.Environ(),
		"XCONNECT_GATEWAY_STATE_DIR="+s.stateDir,
		"XCONNECT_GATEWAY_SIGNED_CONFIG="+signedConfigPath,
	)
	output, err := cmd.CombinedOutput()
	if err != nil {
		return fmt.Errorf("gateway apply command failed: %w (%s)", err, strings.TrimSpace(string(output)))
	}
	return nil
}

func (s *gatewayControlSyncer) renewSession(ctx context.Context, baseURL *url.URL, credential string) (gatewaySessionResponse, error) {
	var out gatewaySessionResponse
	err := s.doJSON(ctx, baseURL, http.MethodPost, "/api/overlay/v1/device/session", "Device "+credential, map[string]string{
		"client_nonce": newGatewayNonce(),
	}, &out)
	return out, err
}

func (s *gatewayControlSyncer) fetchSignedConfig(ctx context.Context, baseURL *url.URL, enrollmentToken string) (gatewaySignedConfig, error) {
	var out gatewaySignedConfig
	err := s.doJSON(ctx, baseURL, http.MethodGet, "/api/overlay/v1/gateway/signed-config", "Bearer "+enrollmentToken, nil, &out)
	return out, err
}

func (s *gatewayControlSyncer) ackSignedConfig(ctx context.Context, baseURL *url.URL, enrollmentToken string, cfg gatewaySignedConfig) error {
	var out struct {
		Acked *bool `json:"acked"`
	}
	path := fmt.Sprintf("/api/overlay/v1/enrollment/signed-config/%d/ack", cfg.Generation)
	payload := map[string]string{
		"config_id":  cfg.ConfigID,
		"device_id":  cfg.GatewayID,
		"applied_at": time.Now().UTC().Truncate(time.Second).Format(time.RFC3339),
	}
	if err := s.doJSON(ctx, baseURL, http.MethodPost, path, "Bearer "+enrollmentToken, payload, &out); err != nil {
		return err
	}
	if out.Acked != nil && !*out.Acked {
		return errors.New("controller rejected gateway signed-config ack")
	}
	return nil
}

func (s *gatewayControlSyncer) doJSON(ctx context.Context, baseURL *url.URL, method, path, authorization string, input, output any) error {
	endpoint, err := url.JoinPath(baseURL.String(), path)
	if err != nil {
		return err
	}
	var body io.Reader
	if input != nil {
		raw, err := json.Marshal(input)
		if err != nil {
			return err
		}
		body = bytes.NewReader(raw)
	}
	req, err := http.NewRequestWithContext(ctx, method, endpoint, body)
	if err != nil {
		return err
	}
	req.Header.Set("Accept", "application/json")
	req.Header.Set("User-Agent", buildUserAgent(s.agent.ID))
	if input != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	if authorization != "" {
		req.Header.Set("Authorization", authorization)
	}
	resp, err := s.httpClient.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		errBody, _ := io.ReadAll(io.LimitReader(resp.Body, 1<<14))
		return fmt.Errorf("gateway overlay endpoint %s returned %s: %s", path, resp.Status, strings.TrimSpace(string(errBody)))
	}
	if output == nil || resp.StatusCode == http.StatusNoContent {
		return nil
	}
	return json.NewDecoder(io.LimitReader(resp.Body, 2<<20)).Decode(output)
}

func newGatewayNonce() string {
	raw := make([]byte, 16)
	_, _ = rand.Read(raw)
	raw[6] = (raw[6] & 0x0f) | 0x40
	raw[8] = (raw[8] & 0x3f) | 0x80
	return fmt.Sprintf("%08x-%04x-%04x-%04x-%012x", raw[0:4], raw[4:6], raw[6:8], raw[8:10], raw[10:16])
}
