package main

import (
	"context"
	"flag"
	"fmt"
	"log/slog"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/ai-workspace-xstream/xconnect-edge-agent/internal/agentmode"
	"github.com/ai-workspace-xstream/xconnect-edge-agent/internal/config"
	"github.com/ai-workspace-xstream/xconnect-edge-agent/internal/telemetry"
)

var (
	serviceName = "xconnect-edge-agent"
	gitCommit   = "unknown"
	buildDate   = "unknown"
)

func main() {
	configPath := flag.String("config", "account-agent.yaml", "path to configuration file")
	showVersion := flag.Bool("v", false, "print version information and exit")
	flag.Parse()

	if *showVersion {
		fmt.Printf("%s %s %s\n", serviceName, gitCommit, buildDate)
		return
	}

	logger := slog.New(slog.NewJSONHandler(os.Stdout, nil))
	shutdownTelemetry, telemetryErr := telemetry.Init(context.Background(), gitCommit)
	if telemetryErr != nil {
		logger.Warn("OpenTelemetry exporter initialization failed; continuing without export", "err", telemetryErr)
	} else {
		defer func() {
			shutdownCtx, shutdownCancel := context.WithTimeout(context.Background(), 2*time.Second)
			defer shutdownCancel()
			if err := shutdownTelemetry(shutdownCtx); err != nil {
				logger.Warn("OpenTelemetry shutdown failed", "err", err)
			}
		}()
	}

	cfg, err := config.Load(*configPath)
	if err != nil {
		logger.Error("failed to load configuration", "path", *configPath, "err", err)
		os.Exit(1)
	}

	if cfg.Mode != "agent" {
		logger.Error("invalid run mode", "expected", "agent", "got", cfg.Mode)
		os.Exit(1)
	}

	ctx, cancel := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer cancel()

	logger.Info("starting agent", "id", cfg.Agent.ID, "role", cfg.Agent.EffectiveRole(), "resourceClass", cfg.Agent.ResourceClass, "teamId", cfg.Agent.TeamID, "pool", cfg.Agent.Pool)

	opts := agentmode.Options{
		Logger:  logger,
		Agent:   cfg.Agent,
		Xray:    cfg.Xray,
		Billing: cfg.Billing,
	}

	if err := agentmode.Run(ctx, opts); err != nil {
		logger.Error("agent runtime failed", "err", err)
		os.Exit(1)
	}

	logger.Info("agent shutdown complete")
}
