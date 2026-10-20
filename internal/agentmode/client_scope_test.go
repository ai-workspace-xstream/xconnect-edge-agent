package agentmode

import (
	"context"
	"fmt"
	"github.com/ai-workspace-xstream/xconnect-edge-agent/internal/config"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

func TestScopedSnapshotAdmission(t *testing.T) {
	for _, tc := range []struct {
		name, class, team, scope string
		allowed                  bool
	}{
		{"free", "free", "", `,"scope":{"resourceClass":"free"}`, true},
		{"plus", "plus", "", `,"scope":{"resourceClass":"plus"}`, true},
		{"pro", "pro", "", `,"scope":{"resourceClass":"pro"}`, true},
		{"single public node", "shared", "", `,"scope":{"resourceClass":"shared"}`, true},
		{"team", "team", "team-a", `,"scope":{"resourceClass":"team","teamId":"team-a"}`, true},
		{"other team", "team", "team-a", `,"scope":{"resourceClass":"team","teamId":"team-b"}`, false},
		{"shared cannot broaden free", "free", "", `,"scope":{"resourceClass":"shared"}`, false},
		{"tenant on public scope", "shared", "", `,"scope":{"resourceClass":"shared","teamId":"team-a"}`, false},
		{"plus rejects free", "plus", "", `,"scope":{"resourceClass":"free"}`, false},
		{"missing scope", "plus", "", "", false},
		{"legacy", "", "", "", true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if r.Header.Get("X-Agent-Resource-Class") != tc.class || r.Header.Get("X-Agent-Team-ID") != tc.team {
					t.Error("missing scope expectations")
				}
				fmt.Fprintf(w, `{"clients":[],"total":0,"revision":"new"%s}`, tc.scope)
			}))
			defer server.Close()
			client, err := NewClient(server.URL, "test-token", ClientOptions{ResourceClass: tc.class, TeamID: tc.team})
			if err != nil {
				t.Fatal(err)
			}
			tracker := newSyncTracker()
			tracker.UpdateFetch(3, "previous", tracker.Snapshot().LastFetch)
			snapshot, err := NewHTTPClientSource(client, tracker).FetchSnapshot(context.Background())
			if (err == nil) != tc.allowed {
				t.Fatalf("admission = %v, allowed = %v", err, tc.allowed)
			}
			if !tc.allowed && (!strings.Contains(err.Error(), "expected resourceClass="+fmt.Sprintf("%q", tc.class)) || strings.Contains(err.Error(), "test-token")) {
				t.Fatalf("scope failure must identify expected class without credentials: %v", err)
			}
			if tc.name == "plus rejects free" && !strings.Contains(err.Error(), `received resourceClass="free"`) {
				t.Fatalf("scope mismatch must identify received class: %v", err)
			}
			if !tc.allowed && (tracker.Snapshot().Revision != "previous" || snapshot.Revision != "") {
				t.Fatal("rejected response changed sync state")
			}
			if tc.allowed && snapshot.Revision != "new" {
				t.Fatal("valid empty scoped snapshot must clear removed users")
			}
		})
	}
}

func TestManagedTeamSnapshotAdmission(t *testing.T) {
	for _, tc := range []struct {
		name, body string
		valid      bool
	}{
		{"pending clears users", `{"enrollmentStatus":"pending","scope":{"resourceClass":"team"},"clients":[],"total":0}`, true},
		{"administrator overrides suggestion", `{"enrollmentStatus":"approved","scope":{"resourceClass":"team","teamId":"assigned"},"clients":[],"total":0}`, true},
		{"revoked clears users", `{"enrollmentStatus":"revoked","scope":{"resourceClass":"team"},"clients":[],"total":0}`, true},
		{"pending cannot deliver users", `{"enrollmentStatus":"pending","scope":{"resourceClass":"team"},"clients":[{"id":"test"}],"total":1}`, false},
		{"approved requires team", `{"enrollmentStatus":"approved","scope":{"resourceClass":"team"},"clients":[],"total":0}`, false},
		{"unknown enrollment state", `{"enrollmentStatus":"unexpected","scope":{"resourceClass":"team"},"clients":[],"total":0}`, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { fmt.Fprint(w, tc.body) }))
			defer server.Close()
			client, err := NewClient(server.URL, "test-token", ClientOptions{ResourceClass: "team", TeamID: "suggested"})
			if err != nil {
				t.Fatal(err)
			}
			_, err = client.ListClients(context.Background())
			if (err == nil) != tc.valid {
				t.Fatalf("valid=%v err=%v", tc.valid, err)
			}
		})
	}
}

func TestTeamStatusDoesNotAcknowledgeUnappliedFetch(t *testing.T) {
	now := time.Now().UTC()
	report := buildStatusReport(config.Agent{ID: "team-node", ResourceClass: "team"}, trackerSnapshot{Clients: 0, Revision: "pending-clear", LastSuccess: now.Add(-time.Minute), LastFetch: now}, time.Minute)
	if report.Healthy {
		t.Fatal("fetched revision must not acknowledge an applied empty configuration")
	}
}

func TestManagedTeamEventPollingClosesWithoutWarningError(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("X-Agent-Event-Mode", "poll")
		fmt.Fprint(w, "event: users-changed\ndata: pending-revision\n\n")
	}))
	defer server.Close()
	client, err := NewClient(server.URL, "test-token", ClientOptions{ResourceClass: "team"})
	if err != nil {
		t.Fatal(err)
	}
	seen := ""
	if err := client.WatchUserConfigEvents(context.Background(), func(rev string) { seen = rev }); err != nil {
		t.Fatal(err)
	}
	if seen != "pending-revision" {
		t.Fatal("missing enrollment event")
	}
}
