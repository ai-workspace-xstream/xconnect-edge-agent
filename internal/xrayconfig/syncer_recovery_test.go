package xrayconfig

import (
	"context"
	"errors"
	"sync/atomic"
	"testing"
	"time"
)

type contextErrorClientSource struct {
	calls  atomic.Int32
	failAt int32
	err    error
}

type shutdownClientSource struct {
	started chan struct{}
	calls   atomic.Int32
}

func (s *shutdownClientSource) ListClients(ctx context.Context) ([]Client, error) {
	if s.calls.Add(1) == 1 {
		close(s.started)
	}
	<-ctx.Done()
	return nil, ctx.Err()
}

func TestPeriodicSyncerStopsInFlightRequestOnShutdown(t *testing.T) {
	source := &shutdownClientSource{started: make(chan struct{})}
	syncer, err := NewPeriodicSyncer(PeriodicOptions{
		Interval:  10 * time.Millisecond,
		Source:    source,
		Generator: Generator{Definition: DefaultDefinition(), OutputPath: t.TempDir() + "/config.json"},
	})
	if err != nil {
		t.Fatal(err)
	}
	stop, err := syncer.Start(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	defer stop(ctx)
	select {
	case <-source.started:
	case <-ctx.Done():
		t.Fatal("sync did not start")
	}
	if err := stop(ctx); err != nil {
		t.Fatalf("shutdown: %v", err)
	}
	if got := source.calls.Load(); got != 1 {
		t.Fatalf("retried after shutdown: %d calls", got)
	}
}

func (s *contextErrorClientSource) ListClients(context.Context) ([]Client, error) {
	if s.calls.Add(1) == s.failAt {
		return nil, errors.Join(errors.New("fetch controller clients"), s.err)
	}
	return nil, nil
}

func TestPeriodicSyncerRecoversAfterRequestContextError(t *testing.T) {
	for _, tc := range []struct {
		name string
		err  error
	}{
		{"deadline", context.DeadlineExceeded},
		{"canceled", context.Canceled},
	} {
		t.Run(tc.name, func(t *testing.T) {
			source := &contextErrorClientSource{failAt: 2, err: tc.err}
			results := make(chan SyncResult, 16)
			syncer, err := NewPeriodicSyncer(PeriodicOptions{
				Interval:  10 * time.Millisecond,
				Source:    source,
				Generator: Generator{Definition: DefaultDefinition(), OutputPath: t.TempDir() + "/config.json"},
				OnSync: func(result SyncResult) {
					select {
					case results <- result:
					default:
					}
				},
			})
			if err != nil {
				t.Fatal(err)
			}
			stop, err := syncer.Start(context.Background())
			if err != nil {
				t.Fatal(err)
			}
			t.Cleanup(func() {
				ctx, cancel := context.WithTimeout(context.Background(), time.Second)
				defer cancel()
				if err := stop(ctx); err != nil {
					t.Error(err)
				}
			})
			deadline := time.NewTimer(time.Second)
			defer deadline.Stop()
			for i := 0; i < 3; i++ {
				select {
				case result := <-results:
					if i == 1 && !errors.Is(result.Error, tc.err) {
						t.Fatalf("missing transient error: %v", result.Error)
					}
					if i != 1 && result.Error != nil {
						t.Fatalf("sync %d: %v", i, result.Error)
					}
				case <-deadline.C:
					t.Fatalf("sync loop stopped after request error; observed %d results", i)
				}
			}
		})
	}
}
