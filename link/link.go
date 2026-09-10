// Package link is a pure Go implementation of the Ableton Link protocol
// (https://ableton.github.io/link/): peer discovery, tempo and phase
// synchronization, clock synchronization and transport start/stop sync.
//
// It interoperates on the wire with the official Ableton Link library
// (Live, TRAKTOR, and other Link-enabled applications) without cgo and
// without any Ableton source code.
package link

import (
	"net"
	"sync"
	"time"
)

// Config configures a Link node.
type Config struct {
	// Tempo is the initial tempo in BPM (default 120).
	Tempo float64
	// Enabled starts the node broadcasting immediately (default true).
	Enabled bool
	// StartStopSync enables transport start/stop synchronization.
	StartStopSync bool
	// Interface optionally names the network interface used for discovery
	// (multicast) traffic (default: first non-loopback IPv4 interface).
	Interface string
	// UnicastInterface optionally selects the interface or local IP address
	// that the unicast sockets (peer responses and clock measurement) bind
	// to. Accepts an interface name (e.g. "en0") or an IPv4 literal (e.g.
	// "192.168.1.10"; "0.0.0.0" binds the wildcard address). Defaults to the
	// discovery interface when empty.
	UnicastInterface string
	// Logger receives diagnostics (default: silent).
	Logger Logger
}

func (c Config) withDefaults() Config {
	if c.Tempo <= 0 {
		c.Tempo = 120
	}
	return c
}

// PeerInfo describes a discovered peer.
type PeerInfo struct {
	NodeID    NodeId
	SessionID NodeId
	Tempo     float64
	Addr      *net.UDPAddr
}

// snapshot is a thread-safe copy of the volatile state for the public API.
type snapshot struct {
	enabled   bool
	numPeers  int
	tempo     float64
	playing   bool
	timeline  Timeline
	nodeId    NodeId
	sessionId NodeId
	peers     []PeerInfo
}

// Link is a pure Go Ableton Link peer. Create one with New, then query the
// tempo and phase or modify them like any other Link participant.
type Link struct {
	cfg Config

	mcCh   chan udpPacket
	bcCh   chan udpPacket
	ucCh   chan udpPacket
	msCh   chan udpPacket
	cmdCh  chan func()
	timerC chan uint64
	done   chan struct{}

	engWG sync.WaitGroup
	eng   *engine

	snapMu sync.RWMutex
	snap   snapshot

	cbMu          sync.Mutex
	peersCallback func(int)
	tempoCallback func(float64)
	startStopCb   func(bool)
}

// New creates and starts a Link node.
func New(cfg Config) (*Link, error) {
	cfg = cfg.withDefaults()
	if cfg.Logger == nil {
		cfg.Logger = nopLogger{}
	}

	netw, err := openNetwork(cfg.Interface, cfg.UnicastInterface)
	if err != nil {
		return nil, err
	}

	l := &Link{
		cfg:    cfg,
		mcCh:   make(chan udpPacket, 128),
		bcCh:   make(chan udpPacket, 128),
		ucCh:   make(chan udpPacket, 128),
		msCh:   make(chan udpPacket, 256),
		cmdCh:  make(chan func(), 64),
		timerC: make(chan uint64, 64),
		done:   make(chan struct{}),
	}
	l.snap = snapshot{peers: []PeerInfo{}}

	l.eng = newEngine(cfg, l, netw)
	l.eng.initSessionState()
	l.eng.publishSnapshot()

	l.engWG.Add(1)
	go func() {
		defer l.engWG.Done()
		l.eng.run()
	}()

	pktLog := func(what string) func(error) {
		return func(err error) { cfg.Logger.Warnf("%s recv error: %v", what, err) }
	}
	pktSeen := func(what string) func(int, *net.UDPAddr) {
		return func(n int, from *net.UDPAddr) {
			cfg.Logger.Debugf("%s socket: %d bytes from %v", what, n, from)
		}
	}
	go recvLoop(netw.mcConn, l.mcCh, pktLog("multicast"), pktSeen("multicast"))
	go recvLoop(netw.bcConn, l.bcCh, pktLog("broadcast"), pktSeen("broadcast"))
	go recvLoop(netw.ucConn, l.ucCh, pktLog("unicast"), pktSeen("unicast"))
	go recvLoop(netw.msConn, l.msCh, pktLog("measurement"), pktSeen("measurement"))

	if cfg.Enabled {
		l.Enable(true)
	}

	return l, nil
}

// Close shuts the node down, announces its departure to peers and releases
// the sockets.
func (l *Link) Close() error {
	select {
	case <-l.done:
		return nil // already closed
	default:
	}
	close(l.done)
	l.engWG.Wait()
	l.eng.net.close()
	return nil
}

func (l *Link) command(fn func()) {
	select {
	case l.cmdCh <- fn:
	case <-l.done:
	}
}

// ---------------------------------------------------------------------------
// State queries
// ---------------------------------------------------------------------------

func (l *Link) getSnapshot() snapshot {
	l.snapMu.RLock()
	defer l.snapMu.RUnlock()
	return l.snap
}

// Enabled reports whether the node is currently participating in Link.
func (l *Link) Enabled() bool { return l.getSnapshot().enabled }

// NumPeers returns the number of peers in the current session.
func (l *Link) NumPeers() int { return l.getSnapshot().numPeers }

// Peers returns information about all currently discovered peers.
func (l *Link) Peers() []PeerInfo {
	return append([]PeerInfo(nil), l.getSnapshot().peers...)
}

// Tempo returns the current tempo in BPM.
func (l *Link) Tempo() float64 { return l.getSnapshot().tempo }

// IsPlaying reports the current transport state.
func (l *Link) IsPlaying() bool { return l.getSnapshot().playing }

// NodeID returns this node's identifier.
func (l *Link) NodeID() NodeId { return l.getSnapshot().nodeId }

// SessionID returns the identifier of the session we currently belong to.
func (l *Link) SessionID() NodeId { return l.getSnapshot().sessionId }

// BeatAtTime returns the phase-encoded beat position at the given time with
// respect to the given quantum. The returned value is meaningful for
// synchronization purposes: it aligns between Link peers that share a
// session.
func (l *Link) BeatAtTime(at time.Time, quantum float64) float64 {
	return ToPhaseEncodedBeats(
		l.getSnapshot().timeline,
		MicrosFromTime(at),
		BeatsFromFloat(quantum)).Float()
}

// PhaseAtTime returns the beat phase in [0, quantum) at the given time.
func (l *Link) PhaseAtTime(at time.Time, quantum float64) float64 {
	return Phase(
		BeatsFromFloat(l.BeatAtTime(at, quantum)),
		BeatsFromFloat(quantum)).Float()
}

// TimeAtBeat returns the closest time at which the given phase-encoded beat
// position occurs, with respect to the given quantum.
func (l *Link) TimeAtBeat(beat, quantum float64) time.Time {
	return FromPhaseEncodedBeats(
		l.getSnapshot().timeline,
		BeatsFromFloat(beat),
		BeatsFromFloat(quantum)).Time()
}

// ---------------------------------------------------------------------------
// State modification
// ---------------------------------------------------------------------------

// SetTempo proposes a new tempo in BPM at the given time. The tempo takes
// effect immediately for this node and propagates to all peers of the
// session; peers may outvote it later via the standard Link priority rules.
func (l *Link) SetTempo(bpm float64, atTime time.Time) {
	if bpm < minBPM || bpm > maxBPM {
		return
	}
	l.command(func() { l.eng.applyTempoChange(bpm, MicrosFromTime(atTime)) })
}

// RequestBeatAtTime requests that the given beat occur at the given time,
// respecting the quantum grid when connected to other peers.
func (l *Link) RequestBeatAtTime(beat float64, atTime time.Time, quantum float64) {
	l.command(func() {
		l.eng.applyRequestBeatAtTime(beat, MicrosFromTime(atTime), BeatsFromFloat(quantum))
	})
}

// ForceBeatAtTime forces the given beat to occur at the given time,
// regardless of the quantum grid.
func (l *Link) ForceBeatAtTime(beat float64, atTime time.Time, quantum float64) {
	l.command(func() {
		l.eng.applyForceBeatAtTime(beat, MicrosFromTime(atTime), BeatsFromFloat(quantum))
	})
}

// SetIsPlaying announces the local transport state at the given time. It is
// only propagated to peers when start/stop synchronization is enabled.
func (l *Link) SetIsPlaying(playing bool, atTime time.Time) {
	l.command(func() { l.eng.applyPlayingChange(playing, MicrosFromTime(atTime)) })
}

// ---------------------------------------------------------------------------
// Configuration
// ---------------------------------------------------------------------------

// Enable or disable Link participation. Disabling announces departure
// (bye-bye) and stops all network activity.
func (l *Link) Enable(enable bool) { l.command(func() { l.eng.setEnabled(enable) }) }

// EnableStartStopSync toggles transport start/stop synchronization.
func (l *Link) EnableStartStopSync(enable bool) {
	l.command(func() { l.eng.startStopSync = enable })
}

// StartStopSyncEnabled reports whether start/stop sync is enabled.
func (l *Link) StartStopSyncEnabled() bool {
	l.snapMu.RLock()
	defer l.snapMu.RUnlock()
	return l.eng.startStopSync
}

// ---------------------------------------------------------------------------
// Callbacks
// ---------------------------------------------------------------------------

// SetNumPeersCallback registers a callback invoked when the number of peers
// in the current session changes. It is invoked from the engine goroutine.
func (l *Link) SetNumPeersCallback(cb func(int)) {
	l.cbMu.Lock()
	l.peersCallback = cb
	l.cbMu.Unlock()
}

// SetTempoCallback registers a callback invoked when the session tempo
// changes. It is invoked from the engine goroutine.
func (l *Link) SetTempoCallback(cb func(bpm float64)) {
	l.cbMu.Lock()
	l.tempoCallback = cb
	l.cbMu.Unlock()
}

// SetStartStopCallback registers a callback invoked when the transport state
// changes. It is invoked from the engine goroutine.
func (l *Link) SetStartStopCallback(cb func(playing bool)) {
	l.cbMu.Lock()
	l.startStopCb = cb
	l.cbMu.Unlock()
}

func (l *Link) firePeersCallback(n int) {
	l.cbMu.Lock()
	cb := l.peersCallback
	l.cbMu.Unlock()
	if cb != nil {
		cb(n)
	}
}

func (l *Link) fireTempoCallback(bpm float64) {
	l.cbMu.Lock()
	cb := l.tempoCallback
	l.cbMu.Unlock()
	if cb != nil {
		cb(bpm)
	}
}

func (l *Link) fireStartStopCallback(playing bool) {
	l.cbMu.Lock()
	cb := l.startStopCb
	l.cbMu.Unlock()
	if cb != nil {
		cb(playing)
	}
}

func (l *Link) setSnapshot(s snapshot) {
	l.snapMu.Lock()
	l.snap = s
	l.snapMu.Unlock()
}
