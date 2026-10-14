package xrayconfig

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"
)

type snapshotSource struct {
	mu       sync.Mutex
	snapshot Snapshot
	fetches  int
	// next, when set, replaces the snapshot after every fetch.
	next func(fetch int) Snapshot
}

func (s *snapshotSource) ListClients(context.Context) ([]Client, error) {
	return nil, errors.New("snapshot sources must be fetched through FetchSnapshot")
}

func (s *snapshotSource) FetchSnapshot(context.Context) (Snapshot, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.fetches++
	if s.next != nil {
		s.snapshot = s.next(s.fetches)
	}
	return s.snapshot, nil
}

func (s *snapshotSource) set(snapshot Snapshot) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.snapshot = snapshot
}

// commandLog records commands and fails them on demand.
type commandLog struct {
	mu    sync.Mutex
	calls [][]string
	fail  func(cmd []string) error
}

func (l *commandLog) run(_ context.Context, cmd []string) ([]byte, error) {
	l.mu.Lock()
	l.calls = append(l.calls, append([]string(nil), cmd...))
	fail := l.fail
	l.mu.Unlock()
	if fail != nil {
		return nil, fail(cmd)
	}
	return nil, nil
}

func (l *commandLog) count(prefix string) int {
	l.mu.Lock()
	defer l.mu.Unlock()
	n := 0
	for _, call := range l.calls {
		if strings.Join(call, " ") == prefix || strings.HasPrefix(strings.Join(call, " "), prefix+" ") {
			n++
		}
	}
	return n
}

var (
	alice = Client{ID: "client-a", Email: "a@example.com", Flow: DefaultFlow}
	bob   = Client{ID: "client-b", Email: "b@example.com", Flow: DefaultFlow}
)

func validateCmd() []string {
	return []string{"xray", "run", "-test", "-config", ConfigPathPlaceholder}
}

func newTestSyncer(t *testing.T, source ClientSource, log *commandLog, targets []Target, local *ServerTuning) *PeriodicSyncer {
	t.Helper()
	syncer, err := NewPeriodicSyncer(PeriodicOptions{
		Interval:    time.Minute,
		Source:      source,
		Targets:     targets,
		Runner:      log.run,
		LocalTuning: local,
	})
	if err != nil {
		t.Fatalf("new syncer: %v", err)
	}
	return syncer
}

func readFile(t *testing.T, path string) string {
	t.Helper()
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read %s: %v", path, err)
	}
	return string(data)
}

func leftoverCandidates(t *testing.T, dir string) []string {
	t.Helper()
	matches, err := filepath.Glob(filepath.Join(dir, ".*.candidate-*"))
	if err != nil {
		t.Fatal(err)
	}
	return matches
}

func TestSyncValidatesCandidateBeforeReplacingConfig(t *testing.T) {
	dir := t.TempDir()
	output := filepath.Join(dir, "config.json")
	if err := os.WriteFile(output, []byte("{\"old\": true}\n"), 0o640); err != nil {
		t.Fatal(err)
	}
	source := &snapshotSource{snapshot: Snapshot{Clients: []Client{alice}}}
	log := &commandLog{}
	var validated []string
	log.fail = func(cmd []string) error {
		if cmd[0] != "xray" {
			return nil
		}
		candidate := cmd[len(cmd)-1]
		validated = append(validated, candidate)
		if candidate == output || filepath.Dir(candidate) != dir || !strings.HasSuffix(candidate, ".json") {
			t.Errorf("validate command must point at a .json candidate next to the output, got %q", candidate)
		}
		if !strings.Contains(readFile(t, candidate), alice.ID) {
			t.Error("candidate does not hold the rendered config")
		}
		if readFile(t, output) != "{\"old\": true}\n" {
			t.Error("live config replaced before validation")
		}
		return errors.New("invalid config")
	}
	syncer := newTestSyncer(t, source, log, []Target{{
		Name:            "xhttp",
		Generator:       Generator{Definition: DefaultDefinition(), OutputPath: output},
		ValidateCommand: validateCmd(),
		RestartCommand:  []string{"restart", "xray"},
	}}, nil)

	if _, err := syncer.sync(context.Background()); err == nil || !strings.Contains(err.Error(), "validate config") {
		t.Fatalf("expected a validation failure, got %v", err)
	}
	if len(validated) != 1 {
		t.Fatalf("validate command ran %d times", len(validated))
	}
	if got := readFile(t, output); got != "{\"old\": true}\n" {
		t.Fatalf("validation failure replaced the config: %s", got)
	}
	if info, _ := os.Stat(output); info.Mode().Perm() != 0o640 {
		t.Fatalf("validation failure changed permissions: %v", info.Mode().Perm())
	}
	if log.count("restart xray") != 0 {
		t.Fatal("validation failure restarted xray")
	}
	if syncer.targets[0].lastHash != "" {
		t.Fatal("validation failure recorded the candidate as applied")
	}
	if leftovers := leftoverCandidates(t, dir); len(leftovers) != 0 {
		t.Fatalf("candidate files left behind: %v", leftovers)
	}

	log.fail = nil
	if _, err := syncer.sync(context.Background()); err != nil {
		t.Fatalf("sync after the fix: %v", err)
	}
	if !strings.Contains(readFile(t, output), alice.ID) {
		t.Fatal("validated candidate was not committed")
	}
	if info, _ := os.Stat(output); info.Mode().Perm() != 0o640 {
		t.Fatalf("commit did not keep the file permissions: %v", info.Mode().Perm())
	}
	if log.count("restart xray") != 1 {
		t.Fatalf("expected one restart, got %d", log.count("restart xray"))
	}
	if leftovers := leftoverCandidates(t, dir); len(leftovers) != 0 {
		t.Fatalf("candidate files left behind: %v", leftovers)
	}
}

func TestValidateCommandMustTargetTheCandidate(t *testing.T) {
	output := filepath.Join(t.TempDir(), "config.json")
	source := &snapshotSource{}
	_, err := NewPeriodicSyncer(PeriodicOptions{
		Interval: time.Minute,
		Source:   source,
		Targets: []Target{{
			Name:            "xhttp",
			Generator:       Generator{OutputPath: output},
			ValidateCommand: []string{"xray", "run", "-test", "-config", "/etc/xray/other.json"},
		}},
	})
	if err == nil {
		t.Fatal("a validate command that never sees the candidate must be rejected")
	}

	got, err := validateCommandFor([]string{"xray", "-test", "-config", output}, output, "/tmp/candidate.json")
	if err != nil || got[3] != "/tmp/candidate.json" {
		t.Fatalf("output path argument not redirected to the candidate: %v %v", got, err)
	}
	got, err = validateCommandFor([]string{"xray", "-test", "-config=" + ConfigPathPlaceholder}, output, "/tmp/candidate.json")
	if err != nil || got[2] != "-config=/tmp/candidate.json" {
		t.Fatalf("placeholder not replaced: %v %v", got, err)
	}
}

func TestSyncReportsFailedRecovery(t *testing.T) {
	dir := t.TempDir()
	output := filepath.Join(dir, "config.json")
	source := &snapshotSource{snapshot: Snapshot{Clients: []Client{alice}}}
	log := &commandLog{fail: func([]string) error { return errors.New("unit failed") }}
	syncer := newTestSyncer(t, source, log, []Target{{
		Name:           "xhttp",
		Generator:      Generator{Definition: DefaultDefinition(), OutputPath: output},
		RestartCommand: []string{"restart", "xray"},
	}}, nil)

	// First deploy: there is nothing to go back to.
	_, err := syncer.sync(context.Background())
	if err == nil || !strings.Contains(err.Error(), "no previous config to restore") {
		t.Fatalf("expected the missing previous config to be reported, got %v", err)
	}

	log.fail = nil
	if _, err := syncer.sync(context.Background()); err != nil {
		t.Fatalf("bootstrap: %v", err)
	}
	applied := readFile(t, output)

	// Both the restart and the recovery restart fail.
	log.fail = func([]string) error { return errors.New("unit failed") }
	source.set(Snapshot{Clients: []Client{bob}})
	_, err = syncer.sync(context.Background())
	if err == nil || !strings.Contains(err.Error(), "previous config restored, recovery restart failed") {
		t.Fatalf("expected the failed recovery to be reported, got %v", err)
	}
	if readFile(t, output) != applied {
		t.Fatal("the last applied config was not restored")
	}
}

func TestSyncWithoutChangesLeavesConfigUntouched(t *testing.T) {
	output := filepath.Join(t.TempDir(), "config.json")
	source := &snapshotSource{snapshot: Snapshot{Clients: []Client{alice}}}
	log := &commandLog{}
	syncer := newTestSyncer(t, source, log, []Target{{
		Name:            "xhttp",
		Generator:       Generator{Definition: DefaultDefinition(), OutputPath: output},
		ValidateCommand: validateCmd(),
		RestartCommand:  []string{"restart", "xray"},
	}}, nil)
	if _, err := syncer.sync(context.Background()); err != nil {
		t.Fatalf("bootstrap: %v", err)
	}
	before, err := os.Stat(output)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := syncer.sync(context.Background()); err != nil {
		t.Fatalf("unchanged sync: %v", err)
	}
	after, err := os.Stat(output)
	if err != nil {
		t.Fatal(err)
	}
	if !os.SameFile(before, after) {
		t.Fatal("unchanged snapshot rewrote the config")
	}
	if log.count("xray") != 1 || log.count("restart xray") != 1 {
		t.Fatalf("unchanged snapshot ran commands: %v", log.calls)
	}
}

func TestFirstSyncAdoptsConfigXrayAlreadyRuns(t *testing.T) {
	output := filepath.Join(t.TempDir(), "config.json")
	source := &snapshotSource{snapshot: Snapshot{Clients: []Client{alice}}}
	log := &commandLog{}
	target := []Target{{
		Name:           "xhttp",
		Generator:      Generator{Definition: DefaultDefinition(), OutputPath: output},
		RestartCommand: []string{"restart", "xray"},
	}}
	if _, err := newTestSyncer(t, source, log, target, nil).sync(context.Background()); err != nil {
		t.Fatalf("first agent run: %v", err)
	}
	if log.count("restart xray") != 1 {
		t.Fatalf("first agent run must bootstrap xray, restarts=%d", log.count("restart xray"))
	}

	// An agent restart with the same desired state keeps Xray running.
	restarted := newTestSyncer(t, source, log, target, nil)
	if _, err := restarted.sync(context.Background()); err != nil {
		t.Fatalf("agent restart: %v", err)
	}
	if log.count("restart xray") != 1 {
		t.Fatal("agent restart restarted xray for a config it already runs")
	}
	// The adopted state is the baseline for later deltas.
	source.set(Snapshot{Clients: []Client{alice, bob}})
	adder := &recordingUserAdder{}
	restarted.targets[0].UserAdder = adder
	if _, err := restarted.sync(context.Background()); err != nil {
		t.Fatalf("addition after adoption: %v", err)
	}
	if len(adder.calls) != 1 || len(adder.calls[0]) != 1 || adder.calls[0][0].ID != bob.ID {
		t.Fatalf("addition after adoption was not a delta: %#v", adder.calls)
	}

	// Without a record that Xray loaded the file, it cannot be trusted.
	if err := os.Remove(appliedMarkerPath(output)); err != nil {
		t.Fatal(err)
	}
	if _, err := newTestSyncer(t, source, log, target, nil).sync(context.Background()); err != nil {
		t.Fatalf("agent restart without marker: %v", err)
	}
	if log.count("restart xray") != 2 {
		t.Fatalf("unconfirmed config must restart xray, restarts=%d", log.count("restart xray"))
	}

	// A desired state that differs from the file restarts as before.
	source.set(Snapshot{Clients: []Client{bob}})
	if _, err := newTestSyncer(t, source, log, target, nil).sync(context.Background()); err != nil {
		t.Fatalf("agent restart with changed state: %v", err)
	}
	if log.count("restart xray") != 3 {
		t.Fatalf("changed config must restart xray, restarts=%d", log.count("restart xray"))
	}
}

func policyLevel(t *testing.T, path string) map[string]interface{} {
	t.Helper()
	var root map[string]interface{}
	if err := json.Unmarshal([]byte(readFile(t, path)), &root); err != nil {
		t.Fatal(err)
	}
	policy, _ := root["policy"].(map[string]interface{})
	levels, _ := policy["levels"].(map[string]interface{})
	level, _ := levels["0"].(map[string]interface{})
	if level == nil {
		level = map[string]interface{}{}
	}
	level["loglevel"] = root["log"].(map[string]interface{})["loglevel"]
	return level
}

func TestControllerTuningIsValidatedBeforeOverlayingLocal(t *testing.T) {
	output := filepath.Join(t.TempDir(), "config.json")
	source := &snapshotSource{snapshot: Snapshot{
		Clients: []Client{alice},
		// bufferSizeKB is out of range: the local value must stay, not the
		// template's.
		Tuning: &ServerTuning{BufferSizeKB: intPtr(999999), ConnIdleSeconds: intPtr(300), LogLevel: "loud"},
	}}
	log := &commandLog{}
	local := &ServerTuning{BufferSizeKB: intPtr(128), LogLevel: "error", HandshakeSeconds: intPtr(99)}
	syncer := newTestSyncer(t, source, log, []Target{{
		Name:           "xhttp",
		Generator:      Generator{Definition: DefaultDefinition(), OutputPath: output},
		RestartCommand: []string{"restart", "xray"},
	}}, local)
	if _, err := syncer.sync(context.Background()); err != nil {
		t.Fatalf("sync: %v", err)
	}
	level := policyLevel(t, output)
	if level["bufferSize"] != float64(128) || level["connIdle"] != float64(300) || level["loglevel"] != "error" {
		t.Fatalf("invalid controller fields must fall back to local values: %v", level)
	}
	if _, ok := level["handshake"]; ok {
		t.Fatalf("invalid local field must fall back to the template: %v", level)
	}

	// Withdrawing controller tuning goes back to local, then the template.
	source.set(Snapshot{Clients: []Client{alice}})
	if _, err := syncer.sync(context.Background()); err != nil {
		t.Fatalf("withdraw: %v", err)
	}
	level = policyLevel(t, output)
	if _, ok := level["connIdle"]; ok || level["bufferSize"] != float64(128) {
		t.Fatalf("withdrawn controller tuning not revoked: %v", level)
	}
	if log.count("restart xray") != 2 {
		t.Fatalf("withdrawal must restart xray to apply, restarts=%d", log.count("restart xray"))
	}
}

func TestTargetsShareOneSnapshotAndService(t *testing.T) {
	dir := t.TempDir()
	source := &snapshotSource{next: func(fetch int) Snapshot {
		// Every fetch returns a different desired state.
		return Snapshot{Clients: []Client{{ID: "client-" + string(rune('0'+fetch)), Email: "u@example.com"}}}
	}}
	log := &commandLog{}
	targets := []Target{
		{Name: "xhttp", Generator: Generator{Definition: DefaultDefinition(), OutputPath: filepath.Join(dir, "xhttp.json")}, RestartCommand: []string{"restart", "xray"}},
		{Name: "xhttp-b", Generator: Generator{Definition: DefaultDefinition(), OutputPath: filepath.Join(dir, "xhttp-b.json")}, RestartCommand: []string{"restart", "xray"}},
		{Name: "tcp", Generator: Generator{Definition: JSONDefinition{Raw: []byte(tuningTemplate)}, OutputPath: filepath.Join(dir, "tcp.json")}, RestartCommand: []string{"restart", "xray-tcp"}},
	}
	syncer := newTestSyncer(t, source, log, targets, nil)
	if _, err := syncer.sync(context.Background()); err != nil {
		t.Fatalf("sync: %v", err)
	}
	if source.fetches != 1 {
		t.Fatalf("targets fetched separately: %d fetches", source.fetches)
	}
	for _, target := range targets {
		if !strings.Contains(readFile(t, target.Generator.OutputPath), "client-1") {
			t.Fatalf("target %s rendered from a different snapshot", target.Name)
		}
	}
	if log.count("restart xray") != 1 || log.count("restart xray-tcp") != 1 {
		t.Fatalf("a shared service must restart once per reconciliation: %v", log.calls)
	}
}

func TestSharedServiceCommitsAndRestoresTogether(t *testing.T) {
	dir := t.TempDir()
	first := filepath.Join(dir, "a.json")
	second := filepath.Join(dir, "b.json")
	other := filepath.Join(dir, "tcp.json")
	source := &snapshotSource{snapshot: Snapshot{Clients: []Client{alice}}}
	log := &commandLog{}
	targets := []Target{
		{Name: "a", Generator: Generator{Definition: DefaultDefinition(), OutputPath: first}, ValidateCommand: validateCmd(), RestartCommand: []string{"restart", "xray"}},
		{Name: "b", Generator: Generator{Definition: DefaultDefinition(), OutputPath: second}, ValidateCommand: validateCmd(), RestartCommand: []string{"restart", "xray"}},
		{Name: "tcp", Generator: Generator{Definition: DefaultDefinition(), OutputPath: other}, ValidateCommand: validateCmd(), RestartCommand: []string{"restart", "xray-tcp"}},
	}
	syncer := newTestSyncer(t, source, log, targets, nil)
	if _, err := syncer.sync(context.Background()); err != nil {
		t.Fatalf("bootstrap: %v", err)
	}
	appliedFirst, appliedSecond := readFile(t, first), readFile(t, second)

	// b's candidate is invalid: a must not be committed either, while the
	// independent tcp service still moves forward.
	source.set(Snapshot{Clients: []Client{alice}, Tuning: &ServerTuning{ConnIdleSeconds: intPtr(300)}})
	log.fail = func(cmd []string) error {
		if cmd[0] == "xray" && strings.Contains(filepath.Base(cmd[len(cmd)-1]), ".b.json.") {
			return errors.New("invalid")
		}
		return nil
	}
	_, err := syncer.sync(context.Background())
	if err == nil || !strings.Contains(err.Error(), "target b") {
		t.Fatalf("expected target b to fail validation, got %v", err)
	}
	if readFile(t, first) != appliedFirst || readFile(t, second) != appliedSecond {
		t.Fatal("a shared service was partially committed")
	}
	if log.count("restart xray") != 1 {
		t.Fatal("a failed validation restarted the shared service")
	}
	if !strings.Contains(readFile(t, other), "\"connIdle\": 300") || log.count("restart xray-tcp") != 2 {
		t.Fatal("an independent target was blocked by another service's failure")
	}

	// The restart fails: both files go back and the service restarts once
	// more with them.
	restarts := 0
	log.fail = func(cmd []string) error {
		if strings.Join(cmd, " ") == "restart xray" {
			restarts++
			if restarts == 1 {
				return errors.New("unit failed")
			}
		}
		return nil
	}
	_, err = syncer.sync(context.Background())
	if err == nil || !strings.Contains(err.Error(), "previous config restored and xray restarted") {
		t.Fatalf("expected a reported recovery, got %v", err)
	}
	if readFile(t, first) != appliedFirst || readFile(t, second) != appliedSecond {
		t.Fatal("a failed restart did not restore every file of the service")
	}
	if restarts != 2 {
		t.Fatalf("expected the failed restart and one recovery restart, got %d", restarts)
	}
}
