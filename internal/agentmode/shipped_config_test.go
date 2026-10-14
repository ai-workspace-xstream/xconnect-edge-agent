package agentmode

import (
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/ai-workspace-xstream/xconnect-edge-agent/internal/config"
	"github.com/ai-workspace-xstream/xconnect-edge-agent/internal/xrayconfig"
)

// Every agent config shipped in the repository must produce sync targets the
// syncer accepts, including validate commands that check the candidate.
func TestShippedAgentConfigsBuildValidSyncTargets(t *testing.T) {
	for _, path := range []string{
		"account-agent.yaml",
		"example/account-agent.yaml",
		"config/account-agent.yaml",
		"config/account-agent.cloudrun.yaml",
		"deploy/cloudflare/containers/runtime/account-agent.container.yaml",
	} {
		t.Run(path, func(t *testing.T) {
			file, err := os.Open(filepath.Join("..", "..", path))
			if err != nil {
				t.Fatal(err)
			}
			defer file.Close()
			cfg, err := config.LoadReader(file)
			if err != nil {
				t.Fatalf("load: %v", err)
			}
			if len(cfg.Xray.Sync.Targets) == 0 {
				t.Fatal("no sync targets")
			}
			targets := make([]xrayconfig.Target, 0, len(cfg.Xray.Sync.Targets))
			for _, target := range cfg.Xray.Sync.Targets {
				targets = append(targets, xrayconfig.Target{
					Name:            target.Name,
					Generator:       xrayconfig.Generator{Definition: xrayconfig.DefaultDefinition(), OutputPath: target.OutputPath},
					ValidateCommand: target.ValidateCommand,
					RestartCommand:  target.RestartCommand,
				})
			}
			if _, err := xrayconfig.NewPeriodicSyncer(xrayconfig.PeriodicOptions{
				Interval:    time.Minute,
				Source:      testClientSourceFunc(nil),
				Targets:     targets,
				LocalTuning: cfg.Xray.Sync.Tuning,
			}); err != nil {
				t.Fatalf("sync targets rejected: %v", err)
			}
		})
	}
}
