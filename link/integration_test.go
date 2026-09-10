package link

import (
	"math"
	"testing"
	"time"
)

// TestTwoNodesSync verifies the full discovery + session + measurement +
// tempo/phase sync flow between two Link nodes on the same host. It requires
// a working multicast loopback; run with -short to skip.
func TestTwoNodesSync(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping integration test in short mode")
	}

	// Loopback is used by default so the test works in environments where
	// LAN multicast egress is blocked (VPNs, firewalls). Set
	// ABLINK_TEST_INTERFACE to test on another interface.
	iface := loopbackName(t)

	stderr := func(tag string) Logger {
		return PrintfLogger{Printf: func(f string, a ...any) {
			t.Logf("["+tag+"] "+f, a...)
		}, Debug: false}
	}

	a, err := New(Config{Tempo: 120, Enabled: true, Interface: iface, Logger: stderr("a")})
	skipIfUnavailable(t, err)
	defer a.Close()

	b, err := New(Config{Tempo: 98, Enabled: true, Interface: iface, Logger: stderr("b")})
	skipIfUnavailable(t, err)
	defer b.Close()

	// Wait for both nodes to discover each other and converge to the same
	// session. Note: other Link applications may be running on the network
	// (Live, Traktor, ...); the test only requires that the two test nodes
	// see each other.
	hasPeer := func(l *Link, other *Link) bool {
		for _, p := range l.Peers() {
			if p.NodeID == other.NodeID() {
				return true
			}
		}
		return false
	}

	deadline := time.Now().Add(20 * time.Second)
	for time.Now().Before(deadline) {
		if hasPeer(a, b) && hasPeer(b, a) && a.SessionID() == b.SessionID() {
			break
		}
		time.Sleep(100 * time.Millisecond)
	}
	if !hasPeer(a, b) || !hasPeer(b, a) {
		t.Fatalf("nodes did not discover each other: a=%v b=%v", a.Peers(), b.Peers())
	}
	if a.SessionID() != b.SessionID() {
		t.Fatalf("nodes did not converge to one session: a=%s b=%s", a.SessionID(), b.SessionID())
	}

	// Tempo must be shared within a session. Tempos are quantized on the
	// wire (encoded as whole microseconds per beat), so compare with an
	// epsilon, exactly like the official implementation.
	if math.Abs(a.Tempo()-b.Tempo()) > 0.01 {
		t.Fatalf("tempos diverge: a=%v b=%v", a.Tempo(), b.Tempo())
	}

	// Propose a tempo change on a; b must adopt it.
	a.SetTempo(126, time.Now())
	deadline = time.Now().Add(10 * time.Second)
	for time.Now().Before(deadline) {
		if math.Abs(b.Tempo()-126) < 0.01 {
			break
		}
		time.Sleep(50 * time.Millisecond)
	}
	if math.Abs(b.Tempo()-126) > 0.01 {
		t.Fatalf("tempo change did not propagate: b=%v, want 126", b.Tempo())
	}
	if math.Abs(a.Tempo()-126) > 0.01 {
		t.Fatalf("tempo change not adopted locally: a=%v", a.Tempo())
	}

	// Phase must agree between the two nodes.
	maxDiff := 0.0
	for i := 0; i < 50; i++ {
		now := time.Now()
		pa := a.PhaseAtTime(now, 4)
		pb := b.PhaseAtTime(now, 4)
		diff := pa - pb
		if diff > 2 {
			diff -= 4
		}
		if diff < -2 {
			diff += 4
		}
		if diff < 0 {
			diff = -diff
		}
		if diff > maxDiff {
			maxDiff = diff
		}
		time.Sleep(10 * time.Millisecond)
	}
	if maxDiff > 0.05 {
		t.Fatalf("phase difference too large: %v", maxDiff)
	}

	// Closing one node must eventually unregister it on the other.
	if err := b.Close(); err != nil {
		t.Fatal(err)
	}
	deadline = time.Now().Add(15 * time.Second)
	for time.Now().Before(deadline) {
		if !hasPeer(a, b) {
			break
		}
		time.Sleep(200 * time.Millisecond)
	}
	if hasPeer(a, b) {
		t.Fatalf("node b was not unregistered on a after close: %v", a.Peers())
	}
}
