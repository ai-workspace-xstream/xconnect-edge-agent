package agentmode

import (
	"context"
	"strings"
	"sync"
	"time"

	"github.com/ai-workspace-xstream/xconnect-edge-agent/internal/agentproto"
	"github.com/ai-workspace-xstream/xconnect-edge-agent/internal/xrayconfig"
)

// HTTPClientSource retrieves Xray clients from the controller over HTTP.
type HTTPClientSource struct {
	allowedEmails []string
	client        *Client
	tracker       *syncTracker

	mu     sync.Mutex
	tuning *xrayconfig.ServerTuning
}

// NewHTTPClientSource constructs a source backed by the provided client and
// tracker.
func NewHTTPClientSource(client *Client, tracker *syncTracker) *HTTPClientSource {
	return &HTTPClientSource{client: client, tracker: tracker}
}

// FetchSnapshot implements xrayconfig.SnapshotSource: the clients, tuning and
// revision all come from one controller response.
func (s *HTTPClientSource) FetchSnapshot(ctx context.Context) (xrayconfig.Snapshot, error) {
	resp, err := s.client.ListClients(ctx)
	if err != nil {
		return xrayconfig.Snapshot{}, err
	}
	if len(s.allowedEmails) > 0 && !agentproto.PublicResourceClass(s.client.scope.ResourceClass) {
		allowed := make(map[string]bool, len(s.allowedEmails))
		for _, email := range s.allowedEmails {
			allowed[strings.ToLower(strings.TrimSpace(email))] = true
		}
		clients := make([]xrayconfig.Client, 0, len(resp.Clients))
		for _, client := range resp.Clients {
			if allowed[strings.ToLower(strings.TrimSpace(client.Email))] {
				clients = append(clients, client)
			}
		}
		resp.Clients = clients
	}
	if s.tracker != nil {
		s.tracker.UpdateFetch(len(resp.Clients), resp.Revision, time.Now().UTC())
	}
	s.mu.Lock()
	s.tuning = resp.Tuning
	s.mu.Unlock()
	return xrayconfig.Snapshot{Clients: resp.Clients, Tuning: resp.Tuning, Revision: resp.Revision}, nil
}

// ListClients implements xrayconfig.ClientSource by fetching the latest client
// list via the controller API.
func (s *HTTPClientSource) ListClients(ctx context.Context) ([]xrayconfig.Client, error) {
	snapshot, err := s.FetchSnapshot(ctx)
	if err != nil {
		return nil, err
	}
	return snapshot.Clients, nil
}

// ServerTuning implements xrayconfig.TuningSource with the tuning from the
// latest successful controller response.
func (s *HTTPClientSource) ServerTuning() *xrayconfig.ServerTuning {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.tuning
}
