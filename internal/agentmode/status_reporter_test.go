package agentmode

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/ai-workspace-xstream/xconnect-edge-agent/internal/agentproto"
	"github.com/ai-workspace-xstream/xconnect-edge-agent/internal/config"
)

func TestStatusReporterPublishesSyncChangesBeforeHeartbeat(t *testing.T) {
	reports := make(chan agentproto.StatusReport, 10)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var report agentproto.StatusReport
		if err := json.NewDecoder(r.Body).Decode(&report); err != nil {
			t.Error(err)
			w.WriteHeader(http.StatusBadRequest)
			return
		}
		reports <- report
		w.WriteHeader(http.StatusNoContent)
	}))
	defer server.Close()
	client, err := NewClient(server.URL, "test-token", ClientOptions{})
	if err != nil {
		t.Fatal(err)
	}
	tracker := newSyncTracker()
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	done := make(chan struct{})
	go func() {
		defer close(done)
		runStatusReporter(ctx, client, tracker, config.Agent{ID: "edge"}, time.Hour, time.Minute, slog.New(slog.NewTextHandler(io.Discard, nil)))
	}()
	next := func() agentproto.StatusReport {
		t.Helper()
		select {
		case report := <-reports:
			return report
		case <-time.After(3 * time.Second):
			t.Fatal("status change waited for the hourly heartbeat")
			return agentproto.StatusReport{}
		}
	}
	if next().Healthy {
		t.Fatal("startup must not claim readiness before synchronization")
	}
	tracker.UpdateFetch(19, "free-revision", time.Now())
	tracker.MarkSuccess(time.Now())
	if report := next(); !report.Healthy || !report.Xray.Running || report.Users != 19 || report.SyncRevision != "free-revision" {
		t.Fatalf("sync success not reported: %#v", report)
	}
	tracker.MarkError(errors.New("sync failed"), time.Now())
	if report := next(); report.Healthy || report.Message != "sync failed" {
		t.Fatalf("sync failure not reported: %#v", report)
	}
	tracker.MarkSuccess(time.Now())
	if report := next(); !report.Healthy || report.Message != "" {
		t.Fatalf("sync recovery not reported: %#v", report)
	}
	cancel()
	select {
	case <-done:
	case <-time.After(3 * time.Second):
		t.Fatal("status reporter did not stop")
	}
}

func TestSyncTrackerCoalescesPendingChanges(t *testing.T) {
	tracker := newSyncTracker()
	for i := 0; i < 1000; i++ {
		tracker.MarkSuccess(time.Now())
	}
	if len(tracker.changes) != 1 {
		t.Fatal("sync notifications must be coalesced")
	}
}
