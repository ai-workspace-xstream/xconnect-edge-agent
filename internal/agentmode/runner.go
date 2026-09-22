package agentmode

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"os"
	"strings"
	"sync"
	"time"

	"github.com/ai-workspace-xstream/xconnect-edge-agent/internal/agentproto"
	"github.com/ai-workspace-xstream/xconnect-edge-agent/internal/config"
	"github.com/ai-workspace-xstream/xconnect-edge-agent/internal/xrayconfig"
)

// Options configures the agent runtime.
type Options struct {
	Logger  *slog.Logger
	Agent   config.Agent
	Xray    config.Xray
	Billing config.Billing
}

// Run launches the agent mode control loop. It blocks until the context is
// cancelled or a fatal error occurs during setup.
func Run(ctx context.Context, opts Options) error {
	if ctx == nil {
		return errors.New("context is required")
	}

	logger := opts.Logger
	if logger == nil {
		logger = slog.Default()
	}

	controllerURL := strings.TrimSpace(opts.Agent.ControllerURL)
	if controllerURL == "" {
		return errors.New("agent.controllerUrl is required")
	}
	token := strings.TrimSpace(opts.Agent.APIToken)
	if token == "" {
		return errors.New("agent.apiToken is required")
	}
	if err := opts.Agent.ValidateRole(); err != nil {
		return err
	}

	syncInterval := opts.Agent.SyncInterval
	if syncInterval <= 0 {
		syncInterval = opts.Xray.Sync.Interval
	}
	if syncInterval <= 0 {
		syncInterval = 10 * time.Minute
	}

	statusInterval := opts.Agent.StatusInterval
	if statusInterval <= 0 {
		statusInterval = time.Minute
	}

	httpTimeout := opts.Agent.HTTPTimeout
	if httpTimeout <= 0 {
		httpTimeout = 15 * time.Second
	}

	if opts.Billing.Enabled {
		billingTimeout := opts.Billing.HTTPTimeout
		if billingTimeout <= 0 {
			billingTimeout = httpTimeout
		}
		collectInterval := opts.Billing.CollectInterval
		if collectInterval <= 0 {
			collectInterval = time.Minute
		}
		reconcileInterval := opts.Billing.ReconcileInterval
		if reconcileInterval <= 0 {
			reconcileInterval = 5 * time.Minute
		}

		billingClient, err := NewBillingClient(opts.Billing.BaseURL, billingTimeout)
		if err != nil {
			return err
		}
		startBillingSchedulers(ctx, billingClient, billingScheduleConfig{
			httpTimeout:       billingTimeout,
			collectInterval:   collectInterval,
			reconcileInterval: reconcileInterval,
		}, logger)
	}

	client, err := NewClient(controllerURL, token, ClientOptions{
		Timeout:            httpTimeout,
		InsecureSkipVerify: opts.Agent.TLS.InsecureSkipVerify,
		UserAgent:          buildUserAgent(opts.Agent.ID),
		AgentID:            opts.Agent.ID,
	})
	if err != nil {
		return err
	}

	tracker := newSyncTracker()
	stopFuncs := make([]func(context.Context) error, 0)
	syncers := make([]*xrayconfig.PeriodicSyncer, 0)

	// The legacy Xray synchronizer owns Agent Proxy runtime configuration. A
	// Gateway or One agent must never rewrite it: their Xray and WireGuard
	// processes are owned by xconnect-gateway/xconnect-one respectively.
	// Those roles still authenticate and report their control-plane health.
	if !agentOwnsXraySync(opts.Agent) {
		logger.Info("external data-plane role; skipping agent-proxy Xray synchronization", "role", opts.Agent.EffectiveRole())
		tracker.MarkSuccess(time.Now().UTC())
	} else {
		source := NewHTTPClientSource(client, tracker)

		// If no targets are defined, fallback to the legacy single target.
		targets := opts.Xray.Sync.Targets
		if len(targets) == 0 {
			if opts.Xray.Sync.OutputPath != "" {
				targets = append(targets, config.SyncTarget{
					Name:            "default",
					OutputPath:      opts.Xray.Sync.OutputPath,
					TemplatePath:    opts.Xray.Sync.TemplatePath,
					ValidateCommand: opts.Xray.Sync.ValidateCommand,
					RestartCommand:  opts.Xray.Sync.RestartCommand,
				})
			}
		}

		if len(targets) == 0 {
			// Default to standard location if nothing is configured.
			targets = append(targets, config.SyncTarget{
				Name:       "default",
				OutputPath: "/usr/local/etc/xray/config.json",
			})
		}

		stopFuncs = make([]func(context.Context) error, 0, len(targets))
		syncers = make([]*xrayconfig.PeriodicSyncer, 0, len(targets))

		// Start a syncer for each Agent Proxy target.
		for _, target := range targets {
			outputPath := strings.TrimSpace(target.OutputPath)
			if outputPath == "" {
				logger.Warn("skipping sync target with empty output path", "name", target.Name)
				continue
			}

			generator := xrayconfig.Generator{
				Definition: xrayconfig.DefaultDefinition(),
				OutputPath: outputPath,
				Domain:     opts.Agent.Domain,
			}
			if templatePath := strings.TrimSpace(target.TemplatePath); templatePath != "" {
				payload, err := os.ReadFile(templatePath)
				if err != nil {
					return fmt.Errorf("load xray template %s: %w", templatePath, err)
				}
				generator.Definition = xrayconfig.JSONDefinition{Raw: append([]byte(nil), payload...)}
			}

			var userAdder xrayconfig.UserAdder
			if target.DynamicUsers.Enabled {
				adder, err := xrayconfig.NewCLIUserAdder(target.DynamicUsers.Executable, target.DynamicUsers.Server)
				if err != nil {
					return fmt.Errorf("configure dynamic users for target %s: %w", target.Name, err)
				}
				userAdder = adder
			}

			syncLogger := logger.With("component", "agent-xray-sync", "target", target.Name)
			syncer, err := xrayconfig.NewPeriodicSyncer(xrayconfig.PeriodicOptions{
				Logger:          syncLogger,
				Interval:        syncInterval,
				Source:          source,
				Generator:       generator,
				ValidateCommand: target.ValidateCommand,
				RestartCommand:  target.RestartCommand,
				UserAdder:       userAdder,
				OnSync: func(result xrayconfig.SyncResult) {
					if result.Error != nil {
						tracker.MarkError(result.Error, result.CompletedAt)
						return
					}
					tracker.MarkSuccess(result.CompletedAt)
				},
			})
			if err != nil {
				return err
			}

			stopSync, err := syncer.Start(ctx)
			if err != nil {
				// Clean up already started syncers
				for _, stop := range stopFuncs {
					_ = stop(context.Background())
				}
				return err
			}
			stopFuncs = append(stopFuncs, stopSync)
			syncers = append(syncers, syncer)
		}

		// Controller events are the primary trigger. The sync interval above remains
		// a low-frequency safety net for disconnects, upgrades, and missed events.
		go runUserConfigEventWatcher(ctx, client, syncers, logger)
	}

	defer func() {
		waitCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		for _, stop := range stopFuncs {
			if err := stop(waitCtx); err != nil {
				logger.Warn("xray syncer shutdown", "err", err)
			}
		}
	}()

	reporterCtx, reporterCancel := context.WithCancel(ctx)
	defer reporterCancel()

	var wg sync.WaitGroup
	wg.Add(1)
	go func() {
		defer wg.Done()
		runStatusReporter(reporterCtx, client, tracker, opts.Agent, statusInterval, syncInterval, logger)
	}()

	<-ctx.Done()
	reporterCancel()
	wg.Wait()
	return nil
}

func agentOwnsXraySync(agent config.Agent) bool {
	return agent.EffectiveRole() == config.RoleAgentProxy
}

func runUserConfigEventWatcher(ctx context.Context, client *Client, syncers []*xrayconfig.PeriodicSyncer, logger *slog.Logger) {
	lastRevision := ""
	for ctx.Err() == nil {
		err := client.WatchUserConfigEvents(ctx, func(revision string) {
			if revision == lastRevision {
				return
			}
			lastRevision = revision
			logger.Info("controller user-config event received", "revision", revision)
			for _, syncer := range syncers {
				syncer.Trigger()
			}
		})
		if ctx.Err() != nil {
			return
		}
		logger.Warn("controller user-config event stream unavailable; periodic fallback remains active", "err", err)
		timer := time.NewTimer(30 * time.Second)
		select {
		case <-ctx.Done():
			timer.Stop()
			return
		case <-timer.C:
		}
	}
}

func buildUserAgent(id string) string {
	id = strings.TrimSpace(id)
	if id == "" {
		return "xcontrol-agent"
	}
	return fmt.Sprintf("xcontrol-agent/%s", id)
}

func runStatusReporter(ctx context.Context, client *Client, tracker *syncTracker, agent config.Agent, interval, syncInterval time.Duration, logger *slog.Logger) {
	ticker := time.NewTicker(interval)
	defer ticker.Stop()

	send := func() {
		snapshot := tracker.Snapshot()
		report := buildStatusReport(agent, snapshot, syncInterval)
		if err := client.ReportStatus(ctx, report); err != nil {
			logger.Warn("failed to report agent status", "err", err)
		}
	}

	send()

	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			send()
		}
	}
}

func buildStatusReport(agent config.Agent, snapshot trackerSnapshot, syncInterval time.Duration) agentproto.StatusReport {
	healthy := snapshot.LastError == "" && !snapshot.LastSuccess.IsZero()

	running := false
	var lastSyncPtr *time.Time
	if !snapshot.LastSuccess.IsZero() {
		running = time.Since(snapshot.LastSuccess) <= 3*syncInterval
		last := snapshot.LastSuccess
		lastSyncPtr = &last
	}

	report := agentproto.StatusReport{
		AgentID:      agent.ID,
		Role:         agent.EffectiveRole(),
		Healthy:      healthy,
		Message:      snapshot.LastError,
		HeartbeatAt:  time.Now().UTC(),
		Users:        snapshot.Clients,
		SyncRevision: snapshot.Revision,
		Xray: agentproto.XrayStatus{
			Running: running,
			Clients: snapshot.Clients,
			LastSync: func() *time.Time {
				if lastSyncPtr == nil {
					return nil
				}
				copy := *lastSyncPtr
				return &copy
			}(),
			NodeID:       firstNonEmpty(strings.TrimSpace(agent.NodeID), strings.TrimSpace(agent.ID)),
			NetworkID:    strings.TrimSpace(agent.NetworkID),
			Region:       strings.TrimSpace(agent.Region),
			Pool:         strings.TrimSpace(agent.Pool),
			Provider:     strings.TrimSpace(agent.Provider),
			Product:      strings.TrimSpace(agent.Product),
			LineCode:     strings.TrimSpace(agent.LineCode),
			PricingGroup: strings.TrimSpace(agent.PricingGroup),
			StatsEnabled: agent.StatsEnabled,
			XrayRevision: snapshot.Revision,
		},
	}

	return report
}

func firstNonEmpty(values ...string) string {
	for _, value := range values {
		if strings.TrimSpace(value) != "" {
			return strings.TrimSpace(value)
		}
	}
	return ""
}
