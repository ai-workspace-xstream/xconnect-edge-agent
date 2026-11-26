package agentmode

import "testing"

func TestParseNodeTrafficSelectsExplicitInterface(t *testing.T) {
	counters := "lo: 100 0 0 0 0 0 0 0 200 0 0 0 0 0 0 0\n eth0: 123 0 0 0 0 0 0 0 456 0 0 0 0 0 0 0\n eth1: 1\n"
	rx, tx, err := parseNodeTraffic(counters, "eth0")
	if err != nil || rx != 123 || tx != 456 {
		t.Fatalf("got rx=%d tx=%d err=%v", rx, tx, err)
	}
	for _, iface := range []string{"eth", "eth1"} {
		if _, _, err := parseNodeTraffic(counters, iface); err == nil {
			t.Errorf("expected error for %q", iface)
		}
	}
}
