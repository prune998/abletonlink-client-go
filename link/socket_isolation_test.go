package link

import (
	"testing"
	"time"
)

// TestSocketLayerIsolation checks that two raw openNetwork instances on the
// same interface can exchange multicast datagrams, without the engine.
func TestSocketLayerIsolation(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping integration test in short mode")
	}
	iface := "lo0"

	netA, err := openNetwork(iface, "")
	if err != nil {
		t.Fatalf("openNetwork a: %v", err)
	}
	defer netA.close()

	netB, err := openNetwork(iface, "")
	if err != nil {
		t.Fatalf("openNetwork b: %v", err)
	}
	defer netB.close()

	chB := make(chan udpPacket, 64)
	go recvLoop(netB.mcConn, chB, nil, nil)
	chSelf := make(chan udpPacket, 64)
	go recvLoop(netA.mcConn, chSelf, nil, nil)

	time.Sleep(200 * time.Millisecond)

	netA.sendMulticast([]byte("hello-from-a"))
	netA.sendMulticast([]byte("hello-from-a"))

	deadline := time.Now().Add(3 * time.Second)
	gotSelf, gotB := false, false
	for time.Now().Before(deadline) {
		select {
		case p := <-chSelf:
			t.Logf("a received own loopback: %q from %v", p.data, p.from)
			gotSelf = true
		case p := <-chB:
			t.Logf("b received: %q from %v", p.data, p.from)
			gotB = true
		default:
			time.Sleep(50 * time.Millisecond)
		}
		if gotSelf && gotB {
			break
		}
	}
	if !gotSelf {
		t.Error("node a did not receive its own looped-back multicast")
	}
	if !gotB {
		t.Error("node b did not receive a's multicast")
	}
}
