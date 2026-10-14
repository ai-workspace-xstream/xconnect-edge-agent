//go:build xrayintegration

package xrayconfig

import (
	"context"
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// Runs the validate command the deploy configs ship, against a real Xray.
// The shipped templates route geoip:private, so XRAY_LOCATION_ASSET must
// hold geoip.dat for them; without it only the asset-free cases run.
func TestXrayValidatesCandidateBeforeCommit(t *testing.T) {
	xray, err := exec.LookPath("xray")
	if err != nil {
		t.Skip("xray binary is required; run with xrayintegration build tag")
	}
	validate := []string{xray, "run", "-test", "-config", ConfigPathPlaceholder}

	cases := []struct {
		name       string
		definition Definition
		tuning     *ServerTuning
		valid      bool
		needsAsset bool
	}{
		{name: "tuned", definition: withoutRouting(t), tuning: &ServerTuning{LogLevel: "warning", BufferSizeKB: intPtr(512), ConnIdleSeconds: intPtr(300), XHTTPMaxEachPostBytes: intPtr(1 << 20), XHTTPMaxBufferedPosts: intPtr(30)}, valid: true},
		{name: "incomplete inbound", definition: JSONDefinition{Raw: []byte(tuningTemplate)}, valid: false},
		{name: "unknown protocol", definition: JSONDefinition{Raw: []byte(`{"inbounds":[{"protocol":"no-such-protocol","settings":{"clients":[]}}]}`)}, valid: false},
		{name: "built-in", definition: DefaultDefinition(), valid: true, needsAsset: true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if tc.needsAsset {
				if dir := os.Getenv("XRAY_LOCATION_ASSET"); dir == "" {
					t.Skip("XRAY_LOCATION_ASSET with geoip.dat is required")
				}
			}
			dir := t.TempDir()
			output := filepath.Join(dir, "config.json")
			previous := []byte("{\"previous\": true}\n")
			if err := os.WriteFile(output, previous, 0o644); err != nil {
				t.Fatal(err)
			}
			restarts := 0
			syncer, err := NewPeriodicSyncer(PeriodicOptions{
				Interval: time.Minute,
				Source:   &snapshotSource{snapshot: Snapshot{Clients: []Client{alice}, Tuning: tc.tuning}},
				Targets: []Target{{
					Name:            tc.name,
					Generator:       Generator{Definition: tc.definition, OutputPath: output},
					ValidateCommand: validate,
					RestartCommand:  []string{"restart"},
				}},
				Runner: func(ctx context.Context, cmd []string) ([]byte, error) {
					if cmd[0] == "restart" {
						restarts++
						return nil, nil
					}
					return exec.CommandContext(ctx, cmd[0], cmd[1:]...).CombinedOutput()
				},
			})
			if err != nil {
				t.Fatal(err)
			}
			_, err = syncer.sync(context.Background())
			written, _ := os.ReadFile(output)
			if tc.valid {
				if err != nil || !strings.Contains(string(written), alice.ID) || restarts != 1 {
					t.Fatalf("valid candidate not committed: err=%v restarts=%d", err, restarts)
				}
				return
			}
			if err == nil || string(written) != string(previous) || restarts != 0 {
				t.Fatalf("invalid candidate committed: err=%v restarts=%d", err, restarts)
			}
		})
	}
}

// withoutRouting is the built-in template minus its geoip rule, which needs
// geoip.dat.
func withoutRouting(t *testing.T) Definition {
	t.Helper()
	root, err := DefaultDefinition().Base()
	if err != nil {
		t.Fatal(err)
	}
	delete(root, "routing")
	raw, err := json.Marshal(root)
	if err != nil {
		t.Fatal(err)
	}
	return JSONDefinition{Raw: raw}
}
