package xrayconfig

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"log/slog"
	"os"
	"os/exec"
	"strings"
	"sync"
	"time"

	"github.com/ai-workspace-xstream/xconnect-edge-agent/internal/telemetry"
	"go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/codes"
	"go.opentelemetry.io/otel/trace"
)

// ClientSource provides the list of active Xray clients to encode in the config.
type ClientSource interface {
	ListClients(ctx context.Context) ([]Client, error)
}

// TuningSource is optionally implemented by a ClientSource that receives
// controller-provided server tuning together with the client list. The value
// reflects the most recent successful ListClients call.
type TuningSource interface {
	ServerTuning() *ServerTuning
}

type commandRunner func(ctx context.Context, cmd []string) ([]byte, error)

// Snapshot is the desired state from one controller response: the clients,
// the controller tuning and the revision, fetched together.
type Snapshot struct {
	Clients  []Client
	Tuning   *ServerTuning
	Revision string
}

// SnapshotSource is optionally implemented by a ClientSource that returns the
// clients, tuning and revision of a single response. Without it the syncer
// calls ListClients and then reads TuningSource.
type SnapshotSource interface {
	FetchSnapshot(ctx context.Context) (Snapshot, error)
}

// Target is one Xray configuration file reconciled by a PeriodicSyncer.
type Target struct {
	Name      string
	Generator Generator
	// ValidateCommand checks a candidate before it replaces the live file.
	// It must reference ConfigPathPlaceholder or the output path, which is
	// replaced by the candidate path.
	ValidateCommand []string
	// RestartCommand reloads Xray. Targets with the same command share one
	// service: their changes are committed together and restart it once.
	RestartCommand []string
}

// PeriodicOptions configures a PeriodicSyncer instance.
type PeriodicOptions struct {
	Logger   *slog.Logger
	Interval time.Duration
	Source   ClientSource
	// Targets are all rendered from the same snapshot on every
	// reconciliation. When empty, Generator, ValidateCommand and
	// RestartCommand describe a single target.
	Targets         []Target
	Generator       Generator
	ValidateCommand []string
	RestartCommand  []string
	Runner          commandRunner
	OnSync          func(SyncResult)
	// LocalTuning comes from the agent config. Local and controller tuning
	// are validated separately; valid controller fields then override local
	// ones field by field.
	LocalTuning *ServerTuning
}

// PeriodicSyncer periodically rebuilds the Xray configurations from the
// controller's desired state.
type PeriodicSyncer struct {
	logger      *slog.Logger
	interval    time.Duration
	source      ClientSource
	runner      commandRunner
	onSync      func(SyncResult)
	localTuning *ServerTuning
	trigger     chan struct{}

	syncMu sync.Mutex
	// groups holds the targets that share a restart command, in config order.
	groups           [][]*targetState
	targets          []*targetState
	lastRemoteTuning *ServerTuning
	remoteRejected   string
}

// targetState is a target with what was last applied to it. It is only
// accessed with syncMu held.
type targetState struct {
	Target
	logger *slog.Logger

	lastHash      string
	lastClients   []Client
	lastTuningKey string
	// lastApplied is the config Xray last loaded successfully; nil until
	// known. A failed restart restores it.
	lastApplied []byte
	// loaded is set while Xray is known to run lastHash: it restarted with
	// that file. Only then is the applied marker kept on disk.
	loaded bool
}

// SyncResult describes the outcome of a synchronization attempt.
type SyncResult struct {
	Clients     int
	Error       error
	CompletedAt time.Time
	Changed     bool
}

// NewPeriodicSyncer constructs a new PeriodicSyncer from the provided options.
func NewPeriodicSyncer(opts PeriodicOptions) (*PeriodicSyncer, error) {
	if opts.Source == nil {
		return nil, errors.New("client source is required")
	}
	if opts.Interval <= 0 {
		return nil, errors.New("interval must be positive")
	}
	logger := opts.Logger
	if logger == nil {
		logger = slog.Default()
	}
	runner := opts.Runner
	if runner == nil {
		runner = defaultCommandRunner
	}
	targets := opts.Targets
	if len(targets) == 0 {
		targets = []Target{{
			Name:            "default",
			Generator:       opts.Generator,
			ValidateCommand: opts.ValidateCommand,
			RestartCommand:  opts.RestartCommand,
		}}
	}
	s := &PeriodicSyncer{
		logger:   logger,
		interval: opts.Interval,
		source:   opts.Source,
		runner:   runner,
		onSync:   opts.OnSync,
		trigger:  make(chan struct{}, 1),
	}
	outputs := make(map[string]string, len(targets))
	groupIndex := make(map[string]int)
	for i, target := range targets {
		outputPath := strings.TrimSpace(target.Generator.OutputPath)
		if outputPath == "" {
			return nil, errors.New("generator output path is required")
		}
		if other, exists := outputs[outputPath]; exists {
			return nil, fmt.Errorf("targets %s and %s share output path %s", other, target.Name, outputPath)
		}
		outputs[outputPath] = target.Name
		if len(target.ValidateCommand) > 0 {
			if _, err := validateCommandFor(target.ValidateCommand, outputPath, outputPath+".candidate.json"); err != nil {
				return nil, fmt.Errorf("target %s: %w", target.Name, err)
			}
		}
		state := &targetState{
			Target: target,
			logger: logger.With("target", target.Name),
		}
		state.ValidateCommand = append([]string(nil), target.ValidateCommand...)
		state.RestartCommand = append([]string(nil), target.RestartCommand...)
		if saved := readExpiryState(outputPath); len(saved) > 0 {
			state.lastClients = saved
		}
		s.targets = append(s.targets, state)
		// Targets without a restart command never share one.
		key := fmt.Sprintf("target:%d", i)
		if len(target.RestartCommand) > 0 {
			key = "restart:" + strings.Join(target.RestartCommand, "\x00")
		}
		if index, ok := groupIndex[key]; ok {
			s.groups[index] = append(s.groups[index], state)
			continue
		}
		groupIndex[key] = len(s.groups)
		s.groups = append(s.groups, []*targetState{state})
	}
	local, rejected := opts.LocalTuning.Normalize()
	if len(rejected) > 0 {
		logger.Warn("ignoring out-of-range local tuning; template values apply", "fields", rejected)
	}
	s.localTuning = local
	return s, nil
}

// effectiveTuning validates the controller tuning on its own and overlays the
// valid fields on the already validated local tuning, so an invalid or
// withdrawn controller field falls back to the local value, then to the
// template.
func (s *PeriodicSyncer) effectiveTuning(remote *ServerTuning) *ServerTuning {
	normalized, rejected := remote.Normalize()
	if key := strings.Join(rejected, ","); key != s.remoteRejected {
		s.remoteRejected = key
		if key != "" {
			s.logger.Warn("ignoring out-of-range controller tuning; local or template values apply", "fields", rejected)
		}
	}
	return s.localTuning.Overlay(normalized)
}

// Trigger requests an immediate reconciliation. Multiple pending events are
// coalesced because every reconciliation fetches the complete desired state.
func (s *PeriodicSyncer) Trigger() {
	if s == nil {
		return
	}
	select {
	case s.trigger <- struct{}{}:
	default:
	}
}

// Start launches the synchronization loop. The returned stop function cancels the
// loop and waits for it to exit, honouring the provided context for the wait.
func (s *PeriodicSyncer) Start(ctx context.Context) (func(context.Context) error, error) {
	if s == nil {
		return nil, errors.New("syncer is nil")
	}
	runCtx, cancel := context.WithCancel(ctx)
	done := make(chan struct{})
	go func() {
		defer close(done)
		s.run(runCtx)
	}()
	stop := func(waitCtx context.Context) error {
		cancel()
		if waitCtx == nil {
			waitCtx = context.Background()
		}
		select {
		case <-done:
			return nil
		case <-waitCtx.Done():
			return waitCtx.Err()
		}
	}
	return stop, nil
}

func (s *PeriodicSyncer) run(ctx context.Context) {
	var expiryTimer *time.Timer
	var expiryChan <-chan time.Time

	resetExpiryTimer := func() {
		if expiryTimer != nil {
			expiryTimer.Stop()
			expiryTimer = nil
		}
		if next, ok := s.nextExpiry(time.Now().UTC()); ok {
			dur := time.Until(next)
			if dur < 0 {
				dur = 0
			}
			expiryTimer = time.NewTimer(dur)
			expiryChan = expiryTimer.C
		} else {
			expiryChan = nil
		}
	}
	defer func() {
		if expiryTimer != nil {
			expiryTimer.Stop()
		}
	}()

	if n, err := s.sync(ctx); err != nil {
		s.notify(SyncResult{Clients: n, Error: err, CompletedAt: time.Now().UTC()})
		if ctx.Err() != nil {
			return
		}
		s.logger.Error("xray config sync failed", "err", err)
	} else {
		s.logger.Info("xray config synchronized", "clients", n)
		s.notify(SyncResult{Clients: n, CompletedAt: time.Now().UTC()})
	}
	resetExpiryTimer()

	ticker := time.NewTicker(s.interval)
	defer ticker.Stop()

	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
		case <-expiryChan:
			s.logger.Info("xray config sync triggered by client expiry timer")
		case <-s.trigger:
			s.logger.Info("xray config sync triggered by controller event")
		}
		{
			n, err := s.sync(ctx)
			resetExpiryTimer()
			if err != nil {
				s.notify(SyncResult{Clients: n, Error: err, CompletedAt: time.Now().UTC()})
				// A controller request or command may have its own deadline.
				// Only shutdown of this loop's context should stop reconciliation.
				if ctx.Err() != nil {
					return
				}
				s.logger.Error("xray config sync failed", "err", err)
				continue
			}
			s.logger.Info("xray config synchronized", "clients", n)
			s.notify(SyncResult{Clients: n, CompletedAt: time.Now().UTC()})
		}
	}
}

func (s *PeriodicSyncer) fetch(ctx context.Context) (Snapshot, error) {
	if source, ok := s.source.(SnapshotSource); ok {
		return source.FetchSnapshot(ctx)
	}
	clients, err := s.source.ListClients(ctx)
	if err != nil {
		return Snapshot{}, err
	}
	snapshot := Snapshot{Clients: clients}
	if source, ok := s.source.(TuningSource); ok {
		snapshot.Tuning = source.ServerTuning()
	}
	return snapshot, nil
}

// sync renders every target from one snapshot. Each target computes its own
// change; targets sharing a restart command are validated, committed and
// restarted together. A failing group does not block the others.
func (s *PeriodicSyncer) sync(ctx context.Context) (count int, syncErr error) {
	ctx, span := otel.Tracer("github.com/ai-workspace-xstream/xconnect-edge-agent/internal/xrayconfig").Start(ctx, "xconnect.agent.config_sync")
	started := time.Now()
	defer span.End()
	defer func() { telemetry.RecordSync(ctx, time.Since(started), syncErr) }()
	span.SetAttributes(attribute.String("xconnect.component", "agent"))
	defer func() {
		span.SetAttributes(attribute.Int64("xconnect.sync.duration_ms", time.Since(started).Milliseconds()))
	}()

	s.syncMu.Lock()
	defer s.syncMu.Unlock()

	snapshot, err := s.fetch(ctx)
	if err != nil {
		span.RecordError(err)
		span.SetStatus(codes.Error, "controller snapshot failed")
		now := time.Now().UTC()
		if s.canSweepDisconnected(now) {
			if swept, sweepErr := s.sweepDisconnected(ctx, now); sweepErr != nil {
				s.logger.Warn("disconnected expiry sweep failed", "err", sweepErr)
			} else if swept > 0 {
				s.logger.Info("disconnected expiry sweep removed expired clients", "swept", swept)
			}
		}
		return 0, fmt.Errorf("list clients: %w", err)
	}
	s.lastRemoteTuning = snapshot.Tuning
	now := time.Now().UTC()
	clients := filterActiveClients(snapshot.Clients, now)
	span.SetAttributes(attribute.Int("xconnect.sync.client_count", len(clients)))
	tuning := s.effectiveTuning(snapshot.Tuning)

	var errs []error
	for _, group := range s.groups {
		if err := s.syncGroup(ctx, group, clients, tuning); err != nil {
			errs = append(errs, err)
		}
	}
	if err := errors.Join(errs...); err != nil {
		span.RecordError(err)
		span.SetStatus(codes.Error, "configuration reconciliation failed")
		return 0, err
	}
	return len(clients), nil
}

// change is a validated candidate for one target that differs from what was
// last applied.
type change struct {
	target    *targetState
	rendered  []byte
	hash      string
	tuningKey string
	candidate string
	previous  fileSnapshot
	// needsRestart is set when only reloading Xray applies the change:
	// the first sync, a tuning change, or a withdrawal without session
	// revocation.
	needsRestart  bool
	tuningChanged bool
	added         []Client
	removed       []Client
}

func (s *PeriodicSyncer) syncGroup(ctx context.Context, group []*targetState, clients []Client, tuning *ServerTuning) error {
	var changes []*change
	defer func() {
		for _, c := range changes {
			if c.candidate != "" {
				_ = os.Remove(c.candidate)
			}
		}
	}()
	for _, target := range group {
		c, err := target.plan(clients, tuning)
		if err != nil {
			return fmt.Errorf("target %s: %w", target.Name, err)
		}
		if c != nil {
			changes = append(changes, c)
		}
	}
	if len(changes) == 0 {
		return nil
	}

	// Nothing is committed until every candidate in the group validates, so
	// a failure keeps the live files, their permissions and the applied
	// state, and restarts nothing.
	for _, c := range changes {
		if err := s.prepare(ctx, c); err != nil {
			return fmt.Errorf("target %s: %w", c.target.Name, err)
		}
	}
	for i, c := range changes {
		if err := commitCandidate(c.candidate, c.target.Generator.OutputPath); err != nil {
			restoreErr := restoreAll(changes[:i])
			return fmt.Errorf("target %s: %w", c.target.Name, errors.Join(err, restoreErr))
		}
		c.candidate = ""
	}

	restartCommand := group[0].RestartCommand
	needsRestart := false
	for _, c := range changes {
		needsRestart = needsRestart || c.needsRestart
	}
	if needsRestart && len(restartCommand) > 0 {
		if err := s.runCommand(ctx, restartCommand, "restart xray"); err != nil {
			return s.recoverRestart(ctx, changes, restartCommand, err)
		}
		// One restart loads every committed file of this service.
		for _, c := range changes {
			if c.tuningChanged {
				c.target.logger.Info("server tuning applied", "tuning", c.tuningKey)
			}
			c.target.markApplied(c, clients, true)
		}
		return nil
	}

	for _, c := range changes {
		// Without a restart, Xray only runs the new config when it already
		// ran the previous one and the change needed no reload.
		c.target.markApplied(c, clients, c.target.loaded && !c.needsRestart)
	}
	return nil
}

// plan renders the target and decides how the change has to be applied. It
// returns nil when the target already runs this config.
func (t *targetState) plan(clients []Client, tuning *ServerTuning) (*change, error) {
	generator := t.Generator
	generator.Tuning = tuning
	rendered, err := generator.Render(clients)
	if err != nil {
		return nil, fmt.Errorf("render config: %w", err)
	}
	c := &change{
		target:    t,
		rendered:  rendered,
		hash:      sha256Hex(rendered),
		tuningKey: tuning.Key(),
	}
	// A failed recovery clears loaded but keeps lastHash for the previous
	// config. A controller rollback to that hash must retry the restart.
	// File-only targets can still skip unchanged writes without load proof.
	if t.lastHash != "" && c.hash == t.lastHash && (t.loaded || len(t.RestartCommand) == 0) {
		t.logger.Info("xray config unchanged, skipping write and restart", "clients", len(clients), "hash", c.hash[:12])
		t.updateExpiryMetadata(clients)
		return nil, nil
	}
	previous, err := readFileSnapshot(generator.OutputPath)
	if err != nil {
		return nil, fmt.Errorf("read current config: %w", err)
	}
	if t.lastHash == "" {
		// After an agent restart, a file this agent already applied is
		// adopted instead of restarting Xray for the same config.
		if previous.exists && bytes.Equal(previous.data, rendered) && readAppliedMarker(generator.OutputPath) == c.hash {
			t.logger.Info("xray already runs the desired config; no restart", "hash", c.hash[:12])
			t.markApplied(c, clients, true)
			return nil, nil
		}
		c.needsRestart = true
	} else {
		c.tuningChanged = c.tuningKey != t.lastTuningKey
		c.added, c.removed = clientDelta(t.lastClients, clients)
		clientSetChanged := len(c.added) > 0 || len(c.removed) > 0
		c.needsRestart = (!t.loaded && len(t.RestartCommand) > 0) || c.tuningChanged || (clientSetChanged && len(t.RestartCommand) > 0)
	}
	// Restore the config Xray last loaded, falling back to what is on disk
	// before this agent has applied anything.
	c.previous = previous
	if t.lastApplied != nil {
		c.previous.exists = true
		c.previous.data = t.lastApplied
		if c.previous.mode == 0 {
			c.previous.mode = generator.fileMode()
		}
	}
	return c, nil
}

// prepare writes the candidate next to the live file and validates it there.
func (s *PeriodicSyncer) prepare(ctx context.Context, c *change) error {
	outputPath := c.target.Generator.OutputPath
	perm := c.target.Generator.FileMode
	if perm == 0 {
		perm = 0o644
		if c.previous.exists && c.previous.mode != 0 {
			perm = c.previous.mode
		}
	}
	candidate, err := writeCandidate(outputPath, c.rendered, perm, c.previous)
	if err != nil {
		return err
	}
	c.candidate = candidate
	if len(c.target.ValidateCommand) == 0 {
		return nil
	}
	cmd, err := validateCommandFor(c.target.ValidateCommand, outputPath, candidate)
	if err != nil {
		return err
	}
	if err := s.runCommand(ctx, cmd, "validate config"); err != nil {
		c.target.logger.Warn("candidate config rejected; keeping the current config", "err", err)
		return err
	}
	return nil
}

// recoverRestart puts back the configs Xray last loaded and restarts it
// again, reporting whether the service recovered. A file the failed change
// created, with no previous config behind it, is removed instead.
func (s *PeriodicSyncer) recoverRestart(ctx context.Context, changes []*change, restartCommand []string, cause error) error {
	// Until the recovery restart succeeds nothing proves what Xray runs.
	for _, c := range changes {
		c.target.setLoaded("")
	}
	if err := restoreAll(changes); err != nil {
		s.logger.Error("restart failed and the previous xray config could not be restored", "restartErr", cause, "restoreErr", err)
		return fmt.Errorf("%w; restore previous config: %v", cause, err)
	}
	restored := false
	for _, c := range changes {
		restored = restored || c.previous.exists
	}
	if !restored {
		s.logger.Error("restart failed; there was no previous xray config, the new config was removed", "err", cause)
		return fmt.Errorf("%w; no previous config, new config removed", cause)
	}
	if err := s.runCommand(ctx, restartCommand, "restart xray with the restored config"); err != nil {
		s.logger.Error("restart failed; previous xray config restored but xray did not restart", "restartErr", cause, "recoveryErr", err)
		return fmt.Errorf("%w; previous config restored, recovery restart failed: %v", cause, err)
	}
	for _, c := range changes {
		if c.previous.exists {
			c.target.setLoaded(sha256Hex(c.previous.data))
		}
	}
	s.logger.Warn("restart failed; previous xray config restored and xray restarted", "err", cause)
	return fmt.Errorf("%w; previous config restored and xray restarted", cause)
}

func restoreAll(changes []*change) error {
	var errs []error
	for _, c := range changes {
		if err := restoreFile(c.target.Generator.OutputPath, c.previous); err != nil {
			errs = append(errs, fmt.Errorf("target %s: %w", c.target.Name, err))
		}
	}
	return errors.Join(errs...)
}

func (t *targetState) markApplied(c *change, clients []Client, loaded bool) {
	t.lastHash = c.hash
	t.lastClients = append([]Client(nil), clients...)
	t.lastTuningKey = c.tuningKey
	t.lastApplied = c.rendered
	if loaded {
		t.setLoaded(c.hash)
	} else {
		t.setLoaded("")
	}
	if err := writeExpiryState(t.Generator.OutputPath, clients); err != nil {
		t.logger.Warn("record applied xray client expiry state", "err", err)
	}
}

func (t *targetState) updateExpiryMetadata(clients []Client) {
	t.lastClients = append([]Client(nil), clients...)
	if err := writeExpiryState(t.Generator.OutputPath, clients); err != nil {
		t.logger.Warn("record applied xray client expiry state", "err", err)
	}
}

// setLoaded records the hash of the config Xray is known to run, or clears
// the record when that is unknown.
func (t *targetState) setLoaded(hash string) {
	t.loaded = hash != "" && hash == t.lastHash
	var err error
	if hash == "" {
		err = removeAppliedMarker(t.Generator.OutputPath)
	} else {
		err = writeAppliedMarker(t.Generator.OutputPath, hash)
	}
	if err != nil {
		t.logger.Warn("record applied xray config", "err", err)
	}
}

func clientDelta(previous, current []Client) (added, removed []Client) {
	previousByID := make(map[string]Client, len(previous))
	currentByID := make(map[string]Client, len(current))
	for _, client := range previous {
		previousByID[client.ID] = client
	}
	for _, client := range current {
		currentByID[client.ID] = client
		old, exists := previousByID[client.ID]
		if !exists || old.Email != client.Email || old.Flow != client.Flow {
			added = append(added, client)
			if exists {
				removed = append(removed, old)
			}
		}
	}
	for id, client := range previousByID {
		if _, exists := currentByID[id]; !exists {
			removed = append(removed, client)
		}
	}
	return added, removed
}

func (s *PeriodicSyncer) notify(result SyncResult) {
	if s.onSync == nil {
		return
	}
	s.onSync(result)
}

func (s *PeriodicSyncer) runCommand(ctx context.Context, cmd []string, action string) error {
	operation := "xconnect.agent.command"
	if strings.Contains(strings.ToLower(action), "restart xray") {
		operation = "xconnect.agent.xray_restart"
	}
	ctx, span := otel.Tracer("github.com/ai-workspace-xstream/xconnect-edge-agent/internal/xrayconfig").Start(ctx, operation)
	started := time.Now()
	defer span.End()
	if operation == "xconnect.agent.xray_restart" {
		span.AddEvent("restart.begin")
	}
	span.SetAttributes(
		attribute.String("xconnect.command.action", action),
		attribute.Int("xconnect.command.arg_count", len(cmd)),
	)
	defer func() {
		span.SetAttributes(attribute.Int64("xconnect.command.duration_ms", time.Since(started).Milliseconds()))
	}()
	output, err := s.runner(ctx, cmd)
	if operation == "xconnect.agent.xray_restart" {
		telemetry.RecordRestart(ctx, time.Since(started), err)
		span.AddEvent("restart.end", trace.WithAttributes(attribute.String("outcome", map[bool]string{true: "error", false: "success"}[err != nil])))
	}
	if err != nil {
		span.RecordError(errors.New("command execution failed"))
		span.SetStatus(codes.Error, "command execution failed")
		if len(output) > 0 {
			return fmt.Errorf("%s: %w: %s", action, err, strings.TrimSpace(string(output)))
		}
		return fmt.Errorf("%s: %w", action, err)
	}
	if len(output) > 0 {
		s.logger.Debug(action, "output", strings.TrimSpace(string(output)))
	}
	return nil
}

func defaultCommandRunner(ctx context.Context, cmd []string) ([]byte, error) {
	if len(cmd) == 0 {
		return nil, errors.New("command is empty")
	}
	c := exec.CommandContext(ctx, cmd[0], cmd[1:]...)
	return c.CombinedOutput()
}

func sha256Hex(b []byte) string {
	h := sha256.Sum256(b)
	return hex.EncodeToString(h[:])
}

// filterActiveClients returns clients whose expiry is either absent or strictly after now.
func filterActiveClients(clients []Client, now time.Time) []Client {
	if len(clients) == 0 {
		return nil
	}
	active := make([]Client, 0, len(clients))
	for _, c := range clients {
		if c.ExpiresAt == nil || c.ExpiresAt.IsZero() || c.ExpiresAt.After(now) {
			active = append(active, c)
		}
	}
	return active
}

// nextExpiry returns the earliest expiration time in the future among all targets' active clients.
func (s *PeriodicSyncer) nextExpiry(now time.Time) (time.Time, bool) {
	var earliest time.Time
	found := false
	for _, target := range s.targets {
		for _, c := range target.lastClients {
			if c.ExpiresAt != nil && !c.ExpiresAt.IsZero() && c.ExpiresAt.After(now) {
				if !found || c.ExpiresAt.Before(earliest) {
					earliest = *c.ExpiresAt
					found = true
				}
			}
		}
	}
	return earliest, found
}

// canSweepDisconnected returns true if any target has clients that have expired according to now.
func (s *PeriodicSyncer) canSweepDisconnected(now time.Time) bool {
	for _, target := range s.targets {
		for _, c := range target.lastClients {
			if c.ExpiresAt != nil && !c.ExpiresAt.IsZero() && !c.ExpiresAt.After(now) {
				return true
			}
		}
	}
	return false
}

// sweepDisconnected reconciles configurations when disconnected from the controller,
// purging clients that have expired according to the local clock.
func (s *PeriodicSyncer) sweepDisconnected(ctx context.Context, now time.Time) (int, error) {
	totalSwept := 0
	var errs []error
	tuning := s.effectiveTuning(s.lastRemoteTuning)

	for _, group := range s.groups {
		if len(group) == 0 {
			continue
		}
		groupSwept := 0
		for _, target := range group {
			active := filterActiveClients(target.lastClients, now)
			if len(active) < len(target.lastClients) {
				groupSwept += len(target.lastClients) - len(active)
			}
		}
		if groupSwept == 0 {
			continue
		}
		activeClients := filterActiveClients(group[0].lastClients, now)
		if err := s.syncGroup(ctx, group, activeClients, tuning); err != nil {
			errs = append(errs, err)
		} else {
			totalSwept += groupSwept
		}
	}
	return totalSwept, errors.Join(errs...)
}
