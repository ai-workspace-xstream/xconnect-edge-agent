package agentmode

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"
)

func TestHTTPClientSourceExposesControllerTuning(t *testing.T) {
	withTuning := true
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/api/agent-server/v1/users" {
			t.Fatalf("unexpected path: %s", r.URL.Path)
		}
		w.Header().Set("Content-Type", "application/json")
		tuning := ""
		if withTuning {
			tuning = `,"tuning":{"logLevel":"warning","bufferSizeKB":512,"futureKnob":true}`
		}
		_, _ = fmt.Fprintf(w, `{"clients":[{"id":"c1","email":"a@example.com"}],"total":1,"revision":"r1"%s}`, tuning)
	}))
	defer server.Close()

	client, err := NewClient(server.URL, "agent-token", ClientOptions{Timeout: time.Second})
	if err != nil {
		t.Fatalf("new client: %v", err)
	}
	source := NewHTTPClientSource(client, nil)
	if _, err := source.ListClients(context.Background()); err != nil {
		t.Fatalf("list clients: %v", err)
	}
	tuning := source.ServerTuning()
	if tuning == nil || tuning.LogLevel != "warning" || tuning.BufferSizeKB == nil || *tuning.BufferSizeKB != 512 {
		t.Fatalf("controller tuning not exposed: %+v", tuning)
	}

	withTuning = false
	if _, err := source.ListClients(context.Background()); err != nil {
		t.Fatalf("list clients: %v", err)
	}
	if source.ServerTuning() != nil {
		t.Fatal("tuning withdrawn by the controller must be cleared")
	}
}
