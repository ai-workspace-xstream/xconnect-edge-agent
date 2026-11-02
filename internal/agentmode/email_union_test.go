package agentmode

import (
	"context"
	"encoding/json"
	"github.com/ai-workspace-xstream/xconnect-edge-agent/internal/agentproto"
	"github.com/ai-workspace-xstream/xconnect-edge-agent/internal/xrayconfig"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"
)

func TestPublicEmailUnionPreservesAuthoritativeSnapshot(t *testing.T) {
	for _, receipt := range []string{"correct", "old-controller", "wrong-list"} {
		t.Run(receipt, func(t *testing.T) {
			emails := []string{"alice@example.com"}
			scope := agentproto.UserScope{ResourceClass: "plus", EmailPolicy: agentproto.PublicEmailUnionPolicy, EmailDigest: agentproto.NodeEmailDigest(emails)}
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if r.Header.Get("X-Agent-Email-Policy") != agentproto.PublicEmailUnionPolicy || r.Header.Get("X-Agent-Allowed-Emails") != `["alice@example.com"]` {
					t.Error("missing normalized policy intent")
				}
				if receipt == "old-controller" {
					scope.EmailPolicy = ""
					scope.EmailDigest = ""
				}
				if receipt == "wrong-list" {
					scope.EmailDigest = "wrong"
				}
				json.NewEncoder(w).Encode(agentproto.ClientListResponse{Scope: &scope, Total: 3, Revision: "revision", Clients: []xrayconfig.Client{{ID: "plus", Email: "paid@example.com"}, {ID: "pro", Email: "pro@example.com"}, {ID: "free", Email: "alice@example.com"}}})
			}))
			defer server.Close()
			client, err := NewClient(server.URL, "test", ClientOptions{ResourceClass: "plus", AllowedEmails: []string{" Alice@Example.COM "}, Timeout: time.Second})
			if err != nil {
				t.Fatal(err)
			}
			source := NewHTTPClientSource(client, nil)
			source.allowedEmails = emails
			snapshot, err := source.FetchSnapshot(context.Background())
			if receipt != "correct" {
				if err == nil {
					t.Fatal("unsupported or mismatched receipt accepted")
				}
				return
			}
			if err != nil || len(snapshot.Clients) != 3 {
				t.Fatalf("union became intersection: count=%d err=%v", len(snapshot.Clients), err)
			}
		})
	}
}
