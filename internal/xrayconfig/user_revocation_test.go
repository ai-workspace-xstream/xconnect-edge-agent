package xrayconfig

import (
	"context"
	"errors"
	"reflect"
	"strings"
	"testing"
)

type fakeRevoker struct {
	calls [][]Client
	err   error
}

func (r *fakeRevoker) RevokeUsers(_ context.Context, _ Generator, c []Client) error {
	r.calls = append(r.calls, append([]Client(nil), c...))
	return r.err
}

func TestSessionRevocationAvoidsRestartAndRetriesFailures(t *testing.T) {
	a := Client{ID: "00000000-0000-4000-8000-000000000001", Email: "a@example.test"}
	b := Client{ID: "00000000-0000-4000-8000-000000000002", Email: "b@example.test"}
	source := &mutableClientSource{clients: []Client{a, b}}
	revoker := &fakeRevoker{}
	restarts := 0
	s, err := NewPeriodicSyncer(PeriodicOptions{Interval: 1, Source: source, Generator: Generator{Definition: DefaultDefinition(), OutputPath: t.TempDir() + "/config.json"}, RestartCommand: []string{"restart"}, Runner: func(context.Context, []string) ([]byte, error) { restarts++; return nil, nil }, UserRevoker: revoker})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := s.sync(context.Background()); err != nil {
		t.Fatal(err)
	}
	source.clients = []Client{b}
	revoker.err = errors.New("unpatched core")
	if _, err := s.sync(context.Background()); err == nil {
		t.Fatal("unsupported server accepted")
	}
	if restarts != 1 {
		t.Fatal("failure restarted unrelated sessions")
	}
	revoker.err = nil
	if _, err := s.sync(context.Background()); err != nil {
		t.Fatal(err)
	}
	if restarts != 1 || len(revoker.calls) != 2 || !reflect.DeepEqual(revoker.calls[1], []Client{a}) {
		t.Fatalf("unexpected revocation/restart: %#v %d", revoker.calls, restarts)
	}
	if _, err := s.sync(context.Background()); err != nil {
		t.Fatal(err)
	}
	if len(revoker.calls) != 2 {
		t.Fatal("replayed event applied again")
	}
}

func TestCLIRevocationRequiresExplicitCapabilityReceipt(t *testing.T) {
	a, err := NewCLIUserAdder("xray", "127.0.0.1:10085")
	if err != nil {
		t.Fatal(err)
	}
	g := Generator{Definition: DefaultDefinition()}
	clients := []Client{{ID: "id", Email: "test@example.test"}}
	a.runner = func(_ context.Context, cmd []string) ([]byte, error) {
		if !strings.Contains(strings.Join(cmd, " "), "api rmus") {
			t.Fatal("stock rmu used")
		}
		return []byte("Revoked 1 user session set(s) in total."), nil
	}
	if err := a.RevokeUsers(context.Background(), g, clients); err != nil {
		t.Fatal(err)
	}
	a.runner = func(context.Context, []string) ([]byte, error) { return []byte("Removed 1 user(s) in total."), nil }
	if err := a.RevokeUsers(context.Background(), g, clients); err == nil {
		t.Fatal("authentication-only receipt accepted")
	}
}
