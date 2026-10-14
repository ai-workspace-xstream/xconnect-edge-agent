package agentmode

import (
	"context"
	"sync"
	"time"

	"github.com/ai-workspace-xstream/xconnect-edge-agent/internal/xrayconfig"
)

// HTTPClientSource retrieves Xray clients from the controller over HTTP.
type HTTPClientSource struct {
	client  *Client
	tracker *syncTracker

	mu     sync.Mutex
	tuning *xrayconfig.ServerTuning
}

// NewHTTPClientSource constructs a source backed by the provided client and
// tracker.
func NewHTTPClientSource(client *Client, tracker *syncTracker) *HTTPClientSource {
	return &HTTPClientSource{client: client, tracker: tracker}
}

// ListClients implements xrayconfig.ClientSource by fetching the latest client
// list via the controller API.
func (s *HTTPClientSource) ListClients(ctx context.Context) ([]xrayconfig.Client, error) {
	resp, err := s.client.ListClients(ctx)
	if err != nil {
		return nil, err
	}
	if s.tracker != nil {
		s.tracker.UpdateFetch(len(resp.Clients), resp.Revision, time.Now().UTC())
	}
	s.mu.Lock()
	s.tuning = resp.Tuning
	s.mu.Unlock()
	return resp.Clients, nil
}

// ServerTuning implements xrayconfig.TuningSource with the tuning from the
// latest successful controller response.
func (s *HTTPClientSource) ServerTuning() *xrayconfig.ServerTuning {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.tuning
}
