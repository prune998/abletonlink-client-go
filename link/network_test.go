package link

import (
	"net"
	"testing"
	"time"
)

func TestResolveUnicastBind(t *testing.T) {
	iface := loopbackName(t)
	lo, err := net.InterfaceByName(iface)
	if err != nil {
		t.Skipf("no %s interface", iface)
	}
	loIP := firstIPv4(t, lo)

	t.Run("empty uses discovery address", func(t *testing.T) {
		got, err := resolveUnicastBind("", loIP)
		if err != nil {
			t.Fatal(err)
		}
		if !got.Equal(loIP) {
			t.Fatalf("got %v, want %v", got, loIP)
		}
	})

	t.Run("interface name", func(t *testing.T) {
		got, err := resolveUnicastBind(iface, loIP)
		if err != nil {
			t.Fatal(err)
		}
		if !got.Equal(loIP) {
			t.Fatalf("got %v, want %v", got, loIP)
		}
	})

	t.Run("IPv4 literal", func(t *testing.T) {
		got, err := resolveUnicastBind("127.0.0.1", loIP)
		if err != nil {
			t.Fatal(err)
		}
		if !got.Equal(net.IPv4(127, 0, 0, 1).To4()) {
			t.Fatalf("got %v, want 127.0.0.1", got)
		}
	})

	t.Run("wildcard", func(t *testing.T) {
		got, err := resolveUnicastBind("0.0.0.0", loIP)
		if err != nil {
			t.Fatal(err)
		}
		if !got.IsUnspecified() {
			t.Fatalf("got %v, want wildcard", got)
		}
	})

	t.Run("unknown interface name", func(t *testing.T) {
		if _, err := resolveUnicastBind("no-such-if0", loIP); err == nil {
			t.Fatal("expected error for unknown interface")
		}
	})

	t.Run("IPv6 literal rejected", func(t *testing.T) {
		if _, err := resolveUnicastBind("fe80::1", loIP); err == nil {
			t.Fatal("expected error for IPv6 literal")
		}
	})
}

func firstIPv4(t *testing.T, ifi *net.Interface) net.IP {
	t.Helper()
	addrs, err := ifi.Addrs()
	if err != nil {
		t.Fatal(err)
	}
	for _, addr := range addrs {
		if ipnet, ok := addr.(*net.IPNet); ok && ipnet.IP.To4() != nil {
			return ipnet.IP.To4()
		}
	}
	t.Fatal("no IPv4 address on interface")
	return nil
}

// TestOpenNetworkUnicastSelection verifies that the unicast sockets bind to
// the requested interface or address while the broadcast socket stays on the
// discovery interface.
func TestOpenNetworkUnicastSelection(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping in short mode")
	}
	iface := loopbackName(t)

	t.Run("explicit IP", func(t *testing.T) {
		n, err := openNetwork(iface, "127.0.0.1")
		skipIfUnavailable(t, err)
		defer n.close()

		ucAddr, _ := n.ucConn.LocalAddr().(*net.UDPAddr)
		msAddr, _ := n.msConn.LocalAddr().(*net.UDPAddr)
		if !ucAddr.IP.Equal(net.IPv4(127, 0, 0, 1).To4()) || ucAddr.Port == 0 {
			t.Fatalf("unicast socket bound to %v", ucAddr)
		}
		if !msAddr.IP.Equal(net.IPv4(127, 0, 0, 1).To4()) || msAddr.Port == 0 {
			t.Fatalf("measurement socket bound to %v", msAddr)
		}
		// Broadcast socket must stay on the discovery interface.
		bcAddr, _ := n.bcConn.LocalAddr().(*net.UDPAddr)
		if !bcAddr.IP.Equal(net.IPv4(127, 0, 0, 1).To4()) {
			t.Fatalf("broadcast socket bound to %v", bcAddr)
		}
	})

	t.Run("wildcard advertises discovery address", func(t *testing.T) {
		n, err := openNetwork(iface, "0.0.0.0")
		skipIfUnavailable(t, err)
		defer n.close()

		ucAddr, _ := n.ucConn.LocalAddr().(*net.UDPAddr)
		if !ucAddr.IP.IsUnspecified() {
			t.Fatalf("unicast socket bound to %v, want wildcard", ucAddr)
		}

		// The advertised measurement endpoint must never be the wildcard
		// address; it is substituted with the discovery interface address.
		ep := n.measurementEndpoint()
		if ep == nil {
			t.Fatal("nil measurement endpoint")
		}
		if ep.IP.IsUnspecified() {
			t.Fatalf("advertised wildcard measurement endpoint: %v", ep)
		}
		if !ep.IP.Equal(net.IPv4(127, 0, 0, 1).To4()) {
			t.Fatalf("advertised endpoint %v, want 127.0.0.1", ep)
		}
		if ep.Port == 0 {
			t.Fatalf("advertised endpoint has no port: %v", ep)
		}
	})

	t.Run("bogus selection fails cleanly", func(t *testing.T) {
		if _, err := openNetwork(iface, "no-such-if42"); err == nil {
			t.Fatal("expected error for bogus unicast interface")
		}
	})
}

// TestUnicastSeparationStillDiscovers checks that a node whose unicast
// sockets are explicitly bound still discovers and synchronizes with a
// default-configured node.
func TestUnicastSeparationStillDiscovers(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping integration test in short mode")
	}
	iface := loopbackName(t)

	a, err := New(Config{Tempo: 120, Enabled: true, Interface: iface})
	skipIfUnavailable(t, err)
	defer a.Close()

	b, err := New(Config{
		Tempo:            100,
		Enabled:          true,
		Interface:        iface,
		UnicastInterface: "127.0.0.1",
	})
	skipIfUnavailable(t, err)
	defer b.Close()

	deadline := time.Now().Add(20 * time.Second)
	for time.Now().Before(deadline) {
		if hasPeerNode(a, b) && hasPeerNode(b, a) && a.SessionID() == b.SessionID() {
			return
		}
		time.Sleep(100 * time.Millisecond)
	}
	t.Fatalf("nodes did not converge: a peers=%v session=%s, b peers=%v session=%s",
		a.Peers(), a.SessionID(), b.Peers(), b.SessionID())
}

func hasPeerNode(l *Link, other *Link) bool {
	for _, p := range l.Peers() {
		if p.NodeID == other.NodeID() {
			return true
		}
	}
	return false
}
