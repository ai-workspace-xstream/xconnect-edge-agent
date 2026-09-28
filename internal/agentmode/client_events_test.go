package agentmode

import (
	"context"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"reflect"
	"sync/atomic"
	"testing"
	"time"

	"github.com/ai-workspace-xstream/xconnect-edge-agent/internal/xrayconfig"
)

func TestWatchUserConfigEvents(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/api/agent-server/v1/users/events" {
			t.Fatalf("unexpected path: %s", r.URL.Path)
		}
		if r.Header.Get("Authorization") != "Bearer agent-token" || r.Header.Get("X-Agent-ID") != "node-1" {
			t.Fatalf("missing agent authentication headers: %#v", r.Header)
		}
		w.Header().Set("Content-Type", "text/event-stream")
		_, _ = fmt.Fprint(w, "event: users-changed\ndata: rev-1\n\n: keepalive\n\ndata: rev-2\n\n")
	}))
	defer server.Close()

	client, err := NewClient(server.URL, "agent-token", ClientOptions{
		Timeout: time.Second,
		AgentID: "node-1",
	})
	if err != nil {
		t.Fatalf("new client: %v", err)
	}
	var revisions []string
	_ = client.WatchUserConfigEvents(context.Background(), func(revision string) {
		revisions = append(revisions, revision)
	})
	if !reflect.DeepEqual(revisions, []string{"rev-1", "rev-2"}) {
		t.Fatalf("unexpected revisions: %#v", revisions)
	}
}

func TestUserConfigSyncIntervalCapsFallbackAtThirtySeconds(t *testing.T) {
	for _, tc := range []struct {
		configured time.Duration
		want       time.Duration
	}{
		{configured: 0, want: 30 * time.Second},
		{configured: time.Minute, want: 30 * time.Second},
		{configured: 10 * time.Second, want: 10 * time.Second},
	} {
		if got := userConfigSyncInterval(tc.configured); got != tc.want {
			t.Errorf("userConfigSyncInterval(%s) = %s, want %s", tc.configured, got, tc.want)
		}
	}
}

func TestUserConfigRevisionDeduperIgnoresReplayedRevisions(t *testing.T) {
	deduper := newUserConfigRevisionDeduper(2)
	if !deduper.Add("rev-1") || deduper.Add("rev-1") || !deduper.Add("rev-2") || deduper.Add("rev-1") {
		t.Fatal("replayed revision was not deduplicated")
	}
	if !deduper.Add("rev-3") || !deduper.Add("rev-1") {
		t.Fatal("bounded revision history did not evict its oldest entry")
	}
}

func TestEventWatcherReconnectsAndDeduplicatesRevision(t *testing.T) {
	var requests atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		request := requests.Add(1)
		w.Header().Set("Content-Type", "text/event-stream")
		flusher := w.(http.Flusher)
		if request == 1 {
			_, _ = fmt.Fprint(w, "data: rev-1\n\n")
			flusher.Flush()
			return
		}
		_, _ = fmt.Fprint(w, "data: rev-1\n\ndata: rev-2\n\n")
		flusher.Flush()
		<-r.Context().Done()
	}))
	defer server.Close()

	client, err := NewClient(server.URL, "agent-token", ClientOptions{Timeout: time.Second})
	if err != nil {
		t.Fatalf("new client: %v", err)
	}
	var reconciliations atomic.Int32
	syncer, err := xrayconfig.NewPeriodicSyncer(xrayconfig.PeriodicOptions{
		Interval: time.Hour,
		Source: testClientSourceFunc(func(context.Context) ([]xrayconfig.Client, error) {
			reconciliations.Add(1)
			return []xrayconfig.Client{{ID: "stable-uuid", Email: "user@example.com"}}, nil
		}),
		Generator:      xrayconfig.Generator{Definition: xrayconfig.DefaultDefinition(), OutputPath: t.TempDir() + "/xray.json"},
		RestartCommand: []string{"restart-xray"},
		Runner:         func(context.Context, []string) ([]byte, error) { return nil, nil },
	})
	if err != nil {
		t.Fatalf("new syncer: %v", err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	stopSync, err := syncer.Start(ctx)
	if err != nil {
		t.Fatalf("start syncer: %v", err)
	}
	defer func() { _ = stopSync(context.Background()) }()
	logger := slog.New(slog.NewTextHandler(io.Discard, nil))
	go runUserConfigEventWatcherWithRetry(ctx, client, []*xrayconfig.PeriodicSyncer{syncer}, logger, time.Millisecond)

	deadline := time.Now().Add(2 * time.Second)
	for reconciliations.Load() < 2 && time.Now().Before(deadline) {
		time.Sleep(time.Millisecond)
	}
	cancel()
	if got := requests.Load(); got < 2 {
		t.Fatalf("event stream did not reconnect, requests=%d", got)
	}
	if got := reconciliations.Load(); got < 2 || got > 3 {
		t.Fatalf("expected initial sync plus one or two reconciliations for two distinct revisions, got %d", got)
	}
}

type testClientSourceFunc func(context.Context) ([]xrayconfig.Client, error)

func (f testClientSourceFunc) ListClients(ctx context.Context) ([]xrayconfig.Client, error) {
	return f(ctx)
}
