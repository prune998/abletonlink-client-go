package link

import (
	"context"
	"errors"
	"fmt"
	"net"

	"golang.org/x/net/ipv4"
)

// Discovery multicast group, mirroring ableton::discovery.
const (
	multicastAddr = "224.76.78.75"
	multicastPort = 20808
)

var multicastGroup = &net.UDPAddr{
	IP:   net.ParseIP(multicastAddr),
	Port: multicastPort,
}

const (
	readBufferSize  = 1 << 20
	writeBufferSize = 1 << 20
)

// network owns the UDP sockets a Link node needs (all IPv4):
//
//   - mcConn: multicast receive socket bound to 0.0.0.0:20808, joined to the
//     Link multicast group on the discovery interface
//   - bcConn: broadcast socket bound to the discovery interface address;
//     sends multicast state broadcasts and receives the unicast responses
//     peers address to it
//   - ucConn: unicast socket bound to the unicast interface; sends unicast
//     responses to peers
//   - msConn: unicast socket for the ping/pong clock measurement protocol,
//     bound to the unicast interface; its address is advertised as the
//     measurement endpoint
type network struct {
	mcConn *net.UDPConn
	bcConn *net.UDPConn
	ucConn *net.UDPConn
	msConn *net.UDPConn

	// ifName/ifIP identify the discovery (multicast) interface.
	ifName string
	ifIP   net.IP

	onError func(error)
}

// selectInterface picks the network interface to use. If name is empty the
// first non-loopback IPv4 interface is used, falling back to the loopback
// interface (useful for local testing).
func selectInterface(name string) (*net.Interface, net.IP, error) {
	ifaces, err := net.Interfaces()
	if err != nil {
		return nil, nil, fmt.Errorf("link: cannot list interfaces: %w", err)
	}

	var fallback *net.Interface
	var fallbackIP net.IP

	for i := range ifaces {
		ifi := &ifaces[i]
		if name != "" && ifi.Name != name {
			continue
		}
		addrs, err := ifi.Addrs()
		if err != nil {
			continue
		}
		var ip net.IP
		for _, addr := range addrs {
			if ipnet, ok := addr.(*net.IPNet); ok && ipnet.IP.To4() != nil {
				ip = ipnet.IP.To4()
				break
			}
		}
		if ip == nil {
			continue
		}
		if ifi.Flags&net.FlagUp == 0 {
			continue
		}
		if ifi.Flags&net.FlagLoopback != 0 {
			if fallback == nil {
				fallback, fallbackIP = ifi, ip
			}
			continue
		}
		return ifi, ip, nil
	}

	if fallback != nil {
		return fallback, fallbackIP, nil
	}
	if name != "" {
		return nil, nil, fmt.Errorf("link: interface %q not found or has no IPv4 address", name)
	}
	return nil, nil, errors.New("link: no usable IPv4 network interface found")
}

// resolveUnicastBind resolves the local IPv4 address that the unicast
// sockets (peer responses and clock measurement) bind to. nameOrIP may be:
//
//   - empty: use the discovery interface's address (the default)
//   - an interface name such as "en0": that interface's first IPv4 address
//   - an IPv4 literal such as "192.168.1.10" or "0.0.0.0": used verbatim
//     ("0.0.0.0" binds the wildcard address so the socket is reachable via
//     every interface)
func resolveUnicastBind(nameOrIP string, discoveryIP net.IP) (net.IP, error) {
	if nameOrIP == "" {
		return discoveryIP, nil
	}

	if ip := net.ParseIP(nameOrIP); ip != nil {
		v4 := ip.To4()
		if v4 == nil {
			return nil, fmt.Errorf("link: unicast interface %q is not an IPv4 address", nameOrIP)
		}
		return v4, nil
	}

	ifi, err := net.InterfaceByName(nameOrIP)
	if err != nil {
		return nil, fmt.Errorf("link: unicast interface %q is neither an interface name nor an IP address", nameOrIP)
	}
	addrs, err := ifi.Addrs()
	if err != nil {
		return nil, fmt.Errorf("link: cannot list addresses of unicast interface %q: %w", nameOrIP, err)
	}
	for _, addr := range addrs {
		if ipnet, ok := addr.(*net.IPNet); ok && ipnet.IP.To4() != nil {
			return ipnet.IP.To4(), nil
		}
	}
	return nil, fmt.Errorf("link: unicast interface %q has no IPv4 address", nameOrIP)
}

// openNetwork opens the sockets for the given (or auto-selected) discovery
// interface. unicastName optionally selects a different interface or local
// IP address for the unicast sockets; when empty they bind to the discovery
// interface's address.
func openNetwork(interfaceName, unicastName string) (*network, error) {
	ifi, ip, err := selectInterface(interfaceName)
	if err != nil {
		return nil, err
	}
	ucIP, err := resolveUnicastBind(unicastName, ip)
	if err != nil {
		return nil, err
	}

	mcConn, err := listenMulticast(ifi, multicastGroup)
	if err != nil {
		return nil, fmt.Errorf("link: cannot join multicast group: %w", err)
	}

	// Broadcast socket: bound to the discovery interface address. Multicast
	// datagrams must egress the interface the group was joined on (macOS
	// rejects sends whose source address does not belong to the egress
	// interface), and peers reply to the broadcast source address, so this
	// socket also receives those unicast responses.
	bcConn, err := net.ListenUDP("udp4", &net.UDPAddr{IP: ip})
	if err != nil {
		_ = mcConn.Close()
		return nil, fmt.Errorf("link: cannot open broadcast socket: %w", err)
	}
	_ = bcConn.SetReadBuffer(readBufferSize)
	_ = bcConn.SetWriteBuffer(writeBufferSize)
	bcPConn := ipv4.NewPacketConn(bcConn)
	_ = bcPConn.SetMulticastInterface(ifi)
	// Loopback so multiple Link applications on the same host can discover
	// each other.
	_ = bcPConn.SetMulticastLoopback(true)

	ucConn, err := net.ListenUDP("udp4", &net.UDPAddr{IP: ucIP})
	if err != nil {
		_ = mcConn.Close()
		_ = bcConn.Close()
		return nil, fmt.Errorf("link: cannot open unicast socket: %w", err)
	}
	_ = ucConn.SetReadBuffer(readBufferSize)
	_ = ucConn.SetWriteBuffer(writeBufferSize)

	msConn, err := net.ListenUDP("udp4", &net.UDPAddr{IP: ucIP})
	if err != nil {
		_ = mcConn.Close()
		_ = bcConn.Close()
		_ = ucConn.Close()
		return nil, fmt.Errorf("link: cannot open measurement socket: %w", err)
	}
	_ = msConn.SetReadBuffer(readBufferSize)
	_ = msConn.SetWriteBuffer(writeBufferSize)

	return &network{
		mcConn: mcConn,
		bcConn: bcConn,
		ucConn: ucConn,
		msConn: msConn,
		ifName: ifi.Name,
		ifIP:   ip,
	}, nil
}

// listenMulticast creates a UDP socket bound to the wildcard address on the
// multicast group's port, joined to the group on the given interface.
//
// SO_REUSEADDR and SO_REUSEPORT are both set (on unix platforms): REUSEPORT
// guarantees the bind succeeds alongside any other Link application's
// sockets (Ableton Live, Traktor, ...), and multicast datagrams are
// delivered to every socket that joined the group on the interface.
func listenMulticast(ifi *net.Interface, group *net.UDPAddr) (*net.UDPConn, error) {
	lc := net.ListenConfig{
		Control: multicastListenControl(),
	}
	pc, err := lc.ListenPacket(context.Background(), "udp4", fmt.Sprintf(":%d", group.Port))
	if err != nil {
		return nil, err
	}
	conn, ok := pc.(*net.UDPConn)
	if !ok {
		_ = pc.Close()
		return nil, errors.New("link: unexpected packet conn type")
	}

	pconn := ipv4.NewPacketConn(conn)
	if err := pconn.JoinGroup(ifi, &net.UDPAddr{IP: multicastGroup.IP}); err != nil {
		_ = conn.Close()
		return nil, err
	}
	return conn, nil
}

func (n *network) close() {
	_ = n.mcConn.Close()
	_ = n.bcConn.Close()
	_ = n.ucConn.Close()
	_ = n.msConn.Close()
}

// multicastEndpoint returns the multicast group endpoint for debugging.
func (n *network) multicastEndpoint() *net.UDPAddr { return multicastGroup }

// measurementEndpoint returns the address to advertise to peers for clock
// measurements: the measurement socket's local address. When that socket is
// bound to the wildcard address, the discovery interface's address is
// substituted, since a wildcard address is not routable for peers.
func (n *network) measurementEndpoint() *net.UDPAddr {
	addr, ok := n.msConn.LocalAddr().(*net.UDPAddr)
	if !ok {
		return nil
	}
	if addr.IP != nil && addr.IP.IsUnspecified() && n.ifIP != nil {
		return &net.UDPAddr{IP: n.ifIP, Port: addr.Port}
	}
	return addr
}

// reportSend forwards a send outcome (nil on success) to the engine's
// rate-limited reporter, which logs failures and recovery transitions.
func (n *network) reportSend(err error) {
	if n.onError != nil {
		n.onError(err)
	}
}

// sendMulticast sends a datagram to the Link multicast group via the
// broadcast socket, which egresses the discovery interface.
func (n *network) sendMulticast(b []byte) {
	_, err := n.bcConn.WriteToUDP(b, multicastGroup)
	n.reportSend(err)
}

// sendTo sends a unicast datagram.
func (n *network) sendTo(b []byte, addr *net.UDPAddr) {
	_, err := n.ucConn.WriteToUDP(b, addr)
	n.reportSend(err)
}

// sendMeasurement sends a unicast datagram via the measurement socket.
func (n *network) sendMeasurement(b []byte, addr *net.UDPAddr) {
	_, err := n.msConn.WriteToUDP(b, addr)
	n.reportSend(err)
}

// recvLoop reads packets from conn until it is closed, forwarding them to
// ch. Packets are dropped if the consumer cannot keep up; Link traffic is
// low-rate and loss-tolerant.
func recvLoop(conn *net.UDPConn, ch chan<- udpPacket, onErr func(error), onPkt func(int, *net.UDPAddr)) {
	buf := make([]byte, maxMessageSize)
	for {
		n, from, err := conn.ReadFromUDP(buf)
		if err != nil {
			if onErr != nil && !errors.Is(err, net.ErrClosed) {
				onErr(err)
			}
			return
		}
		if n == 0 || n > maxMessageSize {
			continue
		}
		if onPkt != nil {
			onPkt(n, from)
		}
		data := make([]byte, n)
		copy(data, buf[:n])
		select {
		case ch <- udpPacket{from: from, data: data}:
		default:
		}
	}
}

// udpPacket is a datagram received from any of the sockets.
type udpPacket struct {
	from *net.UDPAddr
	data []byte
}
