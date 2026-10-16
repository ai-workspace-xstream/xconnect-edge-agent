package agentmode

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestScopedSnapshotAdmission(t *testing.T) {
	for _, tc := range []struct {
		name, class, team, scope string
		allowed                  bool
	}{
		{"free", "free", "", `,"scope":{"resourceClass":"free"}`, true},
		{"plus", "plus", "", `,"scope":{"resourceClass":"plus"}`, true},
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
