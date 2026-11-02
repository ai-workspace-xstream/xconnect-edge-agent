package agentmode

import (
	"context"
	"fmt"
	"github.com/ai-workspace-xstream/xconnect-edge-agent/internal/config"
	"net/http"
	"net/http/httptest"
	"testing"
)

func TestCustomFreeOptInContract(t *testing.T) {
	if err := (config.Agent{ResourceClass: "free", CustomAllowFree: true}).ValidateUserScope(); err == nil {
		t.Fatal("Free opt-in accepted for non-custom node")
	}
	for _, enabled := range []bool{false, true} {
		server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			if (r.Header.Get("X-Agent-Custom-Allow-Free") == "true") != enabled {
				t.Error("opt-in header mismatch")
			}
			fmt.Fprintf(w, `{"scope":{"resourceClass":"custom","customAllowFree":%t},"clients":[],"total":0}`, enabled)
		}))
		c, err := NewClient(server.URL, "test", ClientOptions{ResourceClass: "custom", CustomAllowFree: enabled})
		if err != nil {
			t.Fatal(err)
		}
		if _, err = c.ListClients(context.Background()); err != nil {
			t.Fatal(err)
		}
		server.Close()
	}
}

func TestCustomFreeRejectsControllerIgnoringOptIn(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		fmt.Fprint(w, `{"scope":{"resourceClass":"custom"},"clients":[],"total":0}`)
	}))
	defer server.Close()
	c, err := NewClient(server.URL, "test", ClientOptions{ResourceClass: "custom", CustomAllowFree: true})
	if err != nil {
		t.Fatal(err)
	}
	if _, err = c.ListClients(context.Background()); err == nil {
		t.Fatal("old controller silently accepted opt-in")
	}
}
