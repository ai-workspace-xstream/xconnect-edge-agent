package xrayconfig

import (
	"context"
	"os"
	"path/filepath"
	"reflect"
	"testing"
	"time"
)

func TestFilterActiveClients(t *testing.T) {
	now := time.Date(2026, 11, 7, 12, 0, 0, 0, time.UTC)
	past := now.Add(-10 * time.Minute)
	future := now.Add(10 * time.Minute)

	permanent := Client{ID: "c1", Email: "permanent@example.test"}
	expired := Client{ID: "c2", Email: "expired@example.test", ExpiresAt: &past}
	active := Client{ID: "c3", Email: "active@example.test", ExpiresAt: &future}

	clients := []Client{permanent, expired, active}
	result := filterActiveClients(clients, now)

	if len(result) != 2 {
		t.Fatalf("expected 2 active clients, got %d", len(result))
	}
	if result[0].ID != "c1" || result[1].ID != "c3" {
		t.Fatalf("unexpected active clients: %#v", result)
	}
}

func TestNextExpiryCalculation(t *testing.T) {
	now := time.Date(2026, 11, 7, 12, 0, 0, 0, time.UTC)
	future1 := now.Add(5 * time.Minute)
	future2 := now.Add(15 * time.Minute)
	past := now.Add(-5 * time.Minute)

	syncer := &PeriodicSyncer{
		targets: []*targetState{
			{
				lastClients: []Client{
					{ID: "c1", ExpiresAt: &past},
					{ID: "c2", ExpiresAt: &future2},
				},
			},
			{
				lastClients: []Client{
					{ID: "c3", ExpiresAt: &future1},
					{ID: "c4"}, // permanent
				},
			},
		},
	}

	next, found := syncer.nextExpiry(now)
	if !found {
		t.Fatal("expected next expiry to be found")
	}
	if !next.Equal(future1) {
		t.Fatalf("expected earliest future expiry %v, got %v", future1, next)
	}

	// When all expiries are in the past
	allPastSyncer := &PeriodicSyncer{
		targets: []*targetState{
			{
				lastClients: []Client{{ID: "c1", ExpiresAt: &past}},
			},
		},
	}
	_, foundPast := allPastSyncer.nextExpiry(now)
	if foundPast {
		t.Fatal("expected no next expiry when all are in the past")
	}
}

func TestCanSweepDisconnected(t *testing.T) {
	now := time.Date(2026, 11, 7, 12, 0, 0, 0, time.UTC)
	past := now.Add(-1 * time.Minute)
	future := now.Add(1 * time.Minute)

	syncer := &PeriodicSyncer{
		targets: []*targetState{
			{
				lastClients: []Client{
					{ID: "c1", ExpiresAt: &future},
				},
			},
		},
	}
	if syncer.canSweepDisconnected(now) {
		t.Fatal("expected canSweepDisconnected=false when no clients expired")
	}

	syncer.targets[0].lastClients = append(syncer.targets[0].lastClients, Client{ID: "c2", ExpiresAt: &past})
	if !syncer.canSweepDisconnected(now) {
		t.Fatal("expected canSweepDisconnected=true when expired client present")
	}
}

type mockClientSource struct {
	clients []Client
	err     error
}

func (m *mockClientSource) ListClients(context.Context) ([]Client, error) {
	if m.err != nil {
		return nil, m.err
	}
	return m.clients, nil
}

func TestDisconnectedSweepPurgesExpiredClientsDynamically(t *testing.T) {
	dir := t.TempDir()
	outputPath := filepath.Join(dir, "xray-config.json")
	now := time.Now().UTC()
	past := now.Add(-10 * time.Minute)
	future := now.Add(30 * time.Minute)

	cPermanent := Client{ID: "00000000-0000-4000-8000-000000000001", Email: "perm@example.test"}
	cActive := Client{ID: "00000000-0000-4000-8000-000000000002", Email: "active@example.test", ExpiresAt: &future}
	cExpired := Client{ID: "00000000-0000-4000-8000-000000000003", Email: "expired@example.test", ExpiresAt: &past}

	source := &mockClientSource{clients: []Client{cPermanent, cActive, cExpired}}
	revoker := &fakeRevoker{}
	restarts := 0

	syncer, err := NewPeriodicSyncer(PeriodicOptions{
		Interval:       time.Minute,
		Source:         source,
		Generator:      Generator{Definition: DefaultDefinition(), OutputPath: outputPath},
		RestartCommand: []string{"restart-xray"},
		Runner: func(_ context.Context, _ []string) ([]byte, error) {
			restarts++
			return nil, nil
		},
		UserRevoker: revoker,
	})
	if err != nil {
		t.Fatal(err)
	}

	// 1. Initial sync with live controller: past expiry is filtered out on sync, but let's test sweepDisconnected!
	// Populate target.lastClients with all 3 clients to simulate having been active previously
	syncer.targets[0].lastClients = []Client{cPermanent, cActive, cExpired}
	if err := writeExpiryState(outputPath, syncer.targets[0].lastClients); err != nil {
		t.Fatal(err)
	}
	syncer.targets[0].lastHash = "prev-hash"
	syncer.targets[0].loaded = true

	// 2. Controller is now disconnected (returns error) and clock has passed expiry
	source.err = os.ErrDeadlineExceeded

	if !syncer.canSweepDisconnected(now) {
		t.Fatal("expected canSweepDisconnected=true after clock passed expiry")
	}

	// Perform sync while disconnected
	_, syncErr := syncer.sync(context.Background())
	if syncErr == nil {
		t.Fatal("expected sync to return controller fetch error while disconnected")
	}

	// Check that disconnected sweep ran:
	// cExpired should have been dynamically revoked via revoker without restarting Xray!
	if restarts != 0 {
		t.Fatalf("expected no restart during dynamic revocation, restarts=%d", restarts)
	}
	if len(revoker.calls) != 1 {
		t.Fatalf("expected 1 dynamic revocation call, got %d", len(revoker.calls))
	}
	if len(revoker.calls[0]) != 1 || revoker.calls[0][0].ID != cExpired.ID {
		t.Fatalf("expected revocation of expired client cExpired, got %#v", revoker.calls[0])
	}

	// Check updated .expiry.json does not contain cExpired
	updatedExpiry := readExpiryState(outputPath)
	for _, c := range updatedExpiry {
		if c.ID == cExpired.ID {
			t.Fatalf("expired client %s still present in .expiry.json", cExpired.ID)
		}
	}
}

func TestStartupRestoresClientExpiryState(t *testing.T) {
	dir := t.TempDir()
	outputPath := filepath.Join(dir, "xray-config.json")
	now := time.Date(2026, 11, 7, 12, 0, 0, 0, time.UTC)
	future := now.Add(2 * time.Hour)

	client := Client{
		ID:        "00000000-0000-4000-8000-000000000001",
		Email:     "saved@example.test",
		ExpiresAt: &future,
	}

	// Write expiry state file directly before agent startup
	if err := writeExpiryState(outputPath, []Client{client}); err != nil {
		t.Fatal(err)
	}

	source := &mutableClientSource{}
	syncer, err := NewPeriodicSyncer(PeriodicOptions{
		Interval:  time.Minute,
		Source:    source,
		Generator: Generator{Definition: DefaultDefinition(), OutputPath: outputPath},
	})
	if err != nil {
		t.Fatal(err)
	}

	// Verify startup restored client and expiry metadata into target.lastClients
	if len(syncer.targets[0].lastClients) != 1 {
		t.Fatalf("expected 1 restored client, got %d", len(syncer.targets[0].lastClients))
	}
	restored := syncer.targets[0].lastClients[0]
	if restored.ID != client.ID || restored.Email != client.Email || restored.ExpiresAt == nil || !restored.ExpiresAt.Equal(future) {
		t.Fatalf("restored client mismatch: %#v", restored)
	}
}

func TestGrantRenewalUpdatesExpiryMetadataWithoutRestart(t *testing.T) {
	dir := t.TempDir()
	outputPath := filepath.Join(dir, "xray-config.json")
	now := time.Now().UTC()
	expiry1 := now.Add(1 * time.Hour)
	expiry2 := now.Add(5 * 24 * time.Hour) // renewed for 5 days

	c1 := Client{ID: "00000000-0000-4000-8000-000000000001", Email: "c1@example.test", ExpiresAt: &expiry1}
	source := &mutableClientSource{clients: []Client{c1}}
	restarts := 0

	syncer, err := NewPeriodicSyncer(PeriodicOptions{
		Interval:       time.Minute,
		Source:         source,
		Generator:      Generator{Definition: DefaultDefinition(), OutputPath: outputPath},
		RestartCommand: []string{"restart"},
		Runner: func(_ context.Context, _ []string) ([]byte, error) {
			restarts++
			return nil, nil
		},
	})
	if err != nil {
		t.Fatal(err)
	}

	if _, err := syncer.sync(context.Background()); err != nil {
		t.Fatal(err)
	}
	if restarts != 1 {
		t.Fatalf("expected 1 initial restart, got %d", restarts)
	}

	// Admin renews grant: ID and Email unchanged, ExpiresAt extended
	c1Renewed := Client{ID: "00000000-0000-4000-8000-000000000001", Email: "c1@example.test", ExpiresAt: &expiry2}
	source.clients = []Client{c1Renewed}

	if _, err := syncer.sync(context.Background()); err != nil {
		t.Fatal(err)
	}
	// Content of Xray config was unchanged, so no restart should occur!
	if restarts != 1 {
		t.Fatalf("expected no restart on grant renewal, restarts=%d", restarts)
	}

	// But .expiry.json and lastClients must have updated to expiry2!
	saved := readExpiryState(outputPath)
	if len(saved) != 1 || saved[0].ExpiresAt == nil || !saved[0].ExpiresAt.Equal(expiry2) {
		t.Fatalf("expected renewed expiry %v in .expiry.json, got %#v", expiry2, saved)
	}
	if !reflect.DeepEqual(syncer.targets[0].lastClients[0].ExpiresAt, &expiry2) {
		t.Fatalf("expected target.lastClients to hold renewed expiry %v", expiry2)
	}
}
