package xrayconfig

import (
	"context"
	"errors"
	"os"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

type mutableClientSource struct {
	clients []Client
}

func (s *mutableClientSource) ListClients(context.Context) ([]Client, error) {
	return append([]Client(nil), s.clients...), nil
}

func TestPeriodicSyncerRestartsXrayForClientSetChanges(t *testing.T) {
	first := Client{ID: "client-1", Email: "first@example.com", Flow: DefaultFlow}
	second := Client{ID: "client-2", Email: "second@example.com", Flow: DefaultFlow}
	third := Client{ID: "client-3", Email: "third@example.com", Flow: DefaultFlow}
	source := &mutableClientSource{clients: []Client{first}}
	restarts := 0
	syncer, err := NewPeriodicSyncer(PeriodicOptions{
		Interval: time.Minute,
		Source:   source,
		Generator: Generator{
			Definition: DefaultDefinition(),
			OutputPath: t.TempDir() + "/config.json",
		},
		RestartCommand: []string{"restart-xray"},
		Runner: func(context.Context, []string) ([]byte, error) {
			restarts++
			return nil, nil
		},
	})
	if err != nil {
		t.Fatalf("new syncer: %v", err)
	}

	if _, err := syncer.sync(context.Background()); err != nil {
		t.Fatalf("bootstrap sync: %v", err)
	}
	if restarts != 1 {
		t.Fatalf("bootstrap must restart once, got %d", restarts)
	}

	source.clients = []Client{first, second}
	if _, err := syncer.sync(context.Background()); err != nil {
		t.Fatalf("addition sync: %v", err)
	}
	if restarts != 2 {
		t.Fatalf("client addition must restart Xray, restarts=%d", restarts)
	}

	// Reordering the same set is persisted but does not alter runtime users.
	source.clients = []Client{second, first}
	if _, err := syncer.sync(context.Background()); err != nil {
		t.Fatalf("reorder sync: %v", err)
	}
	if restarts != 2 {
		t.Fatalf("reorder changed runtime: restarts=%d", restarts)
	}

	// Removal restarts only this target's Xray process to enforce withdrawal.
	source.clients = []Client{second}
	if _, err := syncer.sync(context.Background()); err != nil {
		t.Fatalf("removal sync: %v", err)
	}
	if restarts != 3 {
		t.Fatalf("removal must restart Xray, got %d restarts", restarts)
	}
	// A duplicate/replayed event sees the committed desired hash and performs no
	// second restart.
	if _, err := syncer.sync(context.Background()); err != nil {
		t.Fatalf("duplicate removal sync: %v", err)
	}
	if restarts != 3 {
		t.Fatalf("duplicate removal restarted Xray again: %d", restarts)
	}

	// Later unrelated config updates must not re-add the exhausted user.
	source.clients = []Client{second, third}
	if _, err := syncer.sync(context.Background()); err != nil {
		t.Fatalf("sync unrelated addition while first client is paused: %v", err)
	}
	if restarts != 4 {
		t.Fatalf("later client addition must restart Xray once, got %d", restarts)
	}
	rendered, err := os.ReadFile(syncer.targets[0].Generator.OutputPath)
	if err != nil {
		t.Fatalf("read rendered config: %v", err)
	}
	if strings.Contains(string(rendered), first.ID) {
		t.Fatal("paused client's UUID remains in generated config")
	}

	// Quota recovery restores the client by loading the full config after restart.
	source.clients = []Client{first, second, third}
	if _, err := syncer.sync(context.Background()); err != nil {
		t.Fatalf("restore client: %v", err)
	}
	if restarts != 5 {
		t.Fatalf("restoration must restart Xray once, got %d", restarts)
	}
}

func TestPeriodicSyncerRetriesFailedRemovalRestart(t *testing.T) {
	first := Client{ID: "client-1", Email: "first@example.com", Flow: DefaultFlow}
	second := Client{ID: "client-2", Email: "second@example.com", Flow: DefaultFlow}
	source := &mutableClientSource{clients: []Client{first, second}}
	var restarts atomic.Int32
	syncer, err := NewPeriodicSyncer(PeriodicOptions{
		Interval: time.Minute,
		Source:   source,
		Generator: Generator{
			Definition: DefaultDefinition(),
			OutputPath: t.TempDir() + "/config.json",
		},
		RestartCommand: []string{"restart-xray"},
		Runner: func(context.Context, []string) ([]byte, error) {
			if restarts.Add(1) == 2 {
				return nil, errors.New("temporary restart failure")
			}
			return nil, nil
		},
	})
	if err != nil {
		t.Fatalf("new syncer: %v", err)
	}
	if _, err := syncer.sync(context.Background()); err != nil {
		t.Fatalf("bootstrap sync: %v", err)
	}
	committedHash := syncer.targets[0].lastHash
	outputPath := syncer.targets[0].Generator.OutputPath
	applied, err := os.ReadFile(outputPath)
	if err != nil {
		t.Fatal(err)
	}
	source.clients = []Client{second}
	_, err = syncer.sync(context.Background())
	if err == nil || !strings.Contains(err.Error(), "previous config restored and xray restarted") {
		t.Fatalf("expected a reported recovery after the failed restart, got %v", err)
	}
	if got := syncer.targets[0].lastHash; got != committedHash {
		t.Fatalf("failed withdrawal changed committed hash: got %q, want %q", got, committedHash)
	}
	if restored, _ := os.ReadFile(outputPath); string(restored) != string(applied) {
		t.Fatal("failed restart did not restore the last applied config")
	}
	if _, err := syncer.sync(context.Background()); err != nil {
		t.Fatalf("retry removal: %v", err)
	}
	// Initial start, failed withdrawal, recovery restart with the restored
	// config, successful retry.
	if restarts.Load() != 4 {
		t.Fatalf("unexpected restart attempts: %d", restarts.Load())
	}
}

func TestPeriodicSyncerSerializesConcurrentDuplicateRemovals(t *testing.T) {
	first := Client{ID: "client-1", Email: "first@example.com", Flow: DefaultFlow}
	second := Client{ID: "client-2", Email: "second@example.com", Flow: DefaultFlow}
	source := &mutableClientSource{clients: []Client{first, second}}
	var restarts atomic.Int32
	syncer, err := NewPeriodicSyncer(PeriodicOptions{
		Interval: time.Minute,
		Source:   source,
		Generator: Generator{
			Definition: DefaultDefinition(),
			OutputPath: t.TempDir() + "/config.json",
		},
		RestartCommand: []string{"restart-xray"},
		Runner: func(context.Context, []string) ([]byte, error) {
			restarts.Add(1)
			return nil, nil
		},
	})
	if err != nil {
		t.Fatalf("new syncer: %v", err)
	}
	if _, err := syncer.sync(context.Background()); err != nil {
		t.Fatalf("bootstrap sync: %v", err)
	}
	source.clients = []Client{second}
	start := make(chan struct{})
	var wg sync.WaitGroup
	results := make(chan error, 2)
	for range 2 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			<-start
			_, err := syncer.sync(context.Background())
			results <- err
		}()
	}
	close(start)
	wg.Wait()
	close(results)
	for err := range results {
		if err != nil {
			t.Fatalf("concurrent sync: %v", err)
		}
	}
	if restarts.Load() != 2 { // bootstrap plus one merged withdrawal
		t.Fatalf("duplicate concurrent events caused extra restart: %d", restarts.Load())
	}
}

func TestPeriodicSyncerTriggerRunsBeforeFallbackInterval(t *testing.T) {
	var calls atomic.Int32
	source := clientSourceFunc(func(context.Context) ([]Client, error) {
		calls.Add(1)
		return []Client{{ID: "client-1"}}, nil
	})
	syncer, err := NewPeriodicSyncer(PeriodicOptions{
		Interval: time.Hour,
		Source:   source,
		Generator: Generator{
			Definition: DefaultDefinition(),
			OutputPath: t.TempDir() + "/config.json",
		},
	})
	if err != nil {
		t.Fatalf("new syncer: %v", err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	stop, err := syncer.Start(ctx)
	if err != nil {
		t.Fatalf("start syncer: %v", err)
	}
	defer func() { _ = stop(context.Background()) }()

	deadline := time.Now().Add(time.Second)
	for calls.Load() < 1 && time.Now().Before(deadline) {
		time.Sleep(time.Millisecond)
	}
	syncer.Trigger()
	for calls.Load() < 2 && time.Now().Before(deadline) {
		time.Sleep(time.Millisecond)
	}
	if got := calls.Load(); got < 2 {
		t.Fatalf("event trigger did not run reconciliation, calls=%d", got)
	}
}

type clientSourceFunc func(context.Context) ([]Client, error)

func (f clientSourceFunc) ListClients(ctx context.Context) ([]Client, error) {
	return f(ctx)
}
