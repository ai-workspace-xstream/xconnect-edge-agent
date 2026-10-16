package agentmode

import (
	"sync"
	"time"
)

type syncTracker struct {
	mu          sync.RWMutex
	clients     int
	revision    string
	lastFetch   time.Time
	lastSuccess time.Time
	lastError   string
	lastErrorAt time.Time
	changes     chan struct{}
}

func newSyncTracker() *syncTracker {
	return &syncTracker{changes: make(chan struct{}, 1)}
}

func (t *syncTracker) UpdateFetch(count int, revision string, when time.Time) {
	t.mu.Lock()
	defer t.mu.Unlock()

	t.clients = count
	t.revision = revision
	t.lastFetch = when
}

func (t *syncTracker) MarkSuccess(at time.Time) {
	t.mu.Lock()
	defer t.mu.Unlock()

	t.lastSuccess = at
	t.lastError = ""
	t.lastErrorAt = time.Time{}
	t.notifyChange()
}

func (t *syncTracker) MarkError(err error, at time.Time) {
	if err == nil {
		return
	}
	t.mu.Lock()
	defer t.mu.Unlock()

	t.lastError = err.Error()
	t.lastErrorAt = at
	t.notifyChange()
}

// Coalesce sync completions without blocking the synchronizer on an HTTP
// heartbeat. The reporter always reads the latest snapshot when awakened.
func (t *syncTracker) notifyChange() {
	select {
	case t.changes <- struct{}{}:
	default:
	}
}

type trackerSnapshot struct {
	Clients     int
	Revision    string
	LastFetch   time.Time
	LastSuccess time.Time
	LastError   string
	LastErrorAt time.Time
}

func (t *syncTracker) Snapshot() trackerSnapshot {
	t.mu.RLock()
	defer t.mu.RUnlock()

	return trackerSnapshot{
		Clients:     t.clients,
		Revision:    t.revision,
		LastFetch:   t.lastFetch,
		LastSuccess: t.lastSuccess,
		LastError:   t.lastError,
		LastErrorAt: t.lastErrorAt,
	}
}
