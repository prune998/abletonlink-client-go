package link

import (
	"net"
	"sort"
	"time"
)

// Protocol timing constants, mirroring the official Link implementation.
const (
	peerTTLDuration  = 5 * time.Second // discovery TTL advertised in messages
	peerPrunePad     = 1 * time.Second // extra padding before pruning a peer
	pruneInterval    = 1 * time.Second // how often expired peers are pruned
	broadcastPeriod  = 250 * time.Millisecond
	minBroadcastGap  = 50 * time.Millisecond
	sessionEpsilon   = Micros(500000) // 500ms ghost-time epsilon for session switching
	remeasureAfter   = 30 * time.Second
	measureTimer     = 50 * time.Millisecond
	maxMeasurePoints = 100
	maxMeasureStarts = 5
)

// clientStartStop is the client-facing transport state.
type clientStartStop struct {
	isPlaying bool
	time      Micros
	timestamp Micros
}

// peerEntry tracks a discovered peer.
type peerEntry struct {
	state    PeerState
	src      *net.UDPAddr
	lastSeen time.Time
	expireAt time.Time

	// Last (session, value) pairs propagated to the controller, used to
	// detect changes like ableton::link::Peers does.
	tlSession    NodeId
	tlPropagated Timeline
	tlKnown      bool

	ssSession    NodeId
	ssPropagated StartStopState
	ssKnown      bool
}

// sessionEntry is a session other than our current one that we observed.
type sessionEntry struct {
	timeline Timeline
}

// timerHub provides single-shot timers whose callbacks run on the engine
// goroutine.
type timerHub struct {
	ch  chan uint64
	seq uint64
	tim map[uint64]*time.Timer
	cbs map[uint64]func()
}

func newTimerHub(ch chan uint64) *timerHub {
	return &timerHub{ch: ch, tim: make(map[uint64]*time.Timer), cbs: make(map[uint64]func())}
}

func (h *timerHub) after(d time.Duration, fn func()) uint64 {
	h.seq++
	id := h.seq
	h.cbs[id] = fn
	h.tim[id] = time.AfterFunc(d, func() {
		select {
		case h.ch <- id:
		default:
		}
	})
	return id
}

func (h *timerHub) cancel(id uint64) {
	if t, ok := h.tim[id]; ok {
		t.Stop()
		delete(h.tim, id)
	}
	delete(h.cbs, id)
}

func (h *timerHub) fire(id uint64) {
	cb, ok := h.cbs[id]
	if !ok {
		return
	}
	if t, ok := h.tim[id]; ok {
		t.Stop()
		delete(h.tim, id)
	}
	delete(h.cbs, id)
	cb()
}

func (h *timerHub) stopAll() {
	for id, t := range h.tim {
		t.Stop()
		delete(h.tim, id)
		delete(h.cbs, id)
	}
}

// engine implements the Link state machine. All mutable state below is owned
// by the engine goroutine; the public API interacts with it via commands.
type engine struct {
	cfg Config
	log Logger
	l   *Link

	net *network

	// Identity and session state
	nodeId           NodeId
	sessionId        NodeId
	sessionTimeline  Timeline
	sessionXForm     GhostXForm
	sessionStartStop StartStopState
	clientTimeline   Timeline
	clientStartStop  clientStartStop
	lastPlayingCb    bool
	startStopSync    bool
	enabledFlag      bool

	// Network state
	peers          map[NodeId]*peerEntry
	otherSessions  map[NodeId]*sessionEntry
	measurements   map[NodeId]*measurer
	sessionPeerNbr int

	// Broadcast pacing
	lastBroadcast    time.Time
	broadcastPending bool
	broadcastTimerID uint64

	// Periodic session remeasurement
	remeasureID uint64

	hub *timerHub

	// Channels
	mcCh   chan udpPacket
	bcCh   chan udpPacket
	ucCh   chan udpPacket
	msCh   chan udpPacket
	cmdCh  chan func()
	timerC chan uint64
	done   chan struct{}
}

func newEngine(cfg Config, l *Link, netw *network) *engine {
	log := cfg.Logger
	if log == nil {
		log = nopLogger{}
	}
	netw.onError = newSendErrorReporter(log)
	return &engine{
		cfg:           cfg,
		log:           log,
		l:             l,
		net:           netw,
		peers:         make(map[NodeId]*peerEntry),
		otherSessions: make(map[NodeId]*sessionEntry),
		measurements:  make(map[NodeId]*measurer),
		hub:           newTimerHub(l.timerC),
		mcCh:          l.mcCh,
		bcCh:          l.bcCh,
		ucCh:          l.ucCh,
		msCh:          l.msCh,
		cmdCh:         l.cmdCh,
		timerC:        l.timerC,
		done:          l.done,
		startStopSync: cfg.StartStopSync,
	}
}

// newSendErrorReporter returns a rate-limited reporter for socket send
// failures. Broadcasts happen several times per second, so without
// throttling a persistently blocked network would flood the log. The first
// failure is logged with an actionable hint; repeats are summarized at most
// once per reportInterval, and recovery is reported when sends work again.
func newSendErrorReporter(log Logger) func(error) {
	const reportInterval = 30 * time.Second
	var (
		lastReport time.Time
		lastErr    error
		suppressed int
		hintShown  bool
	)
	return func(err error) {
		now := time.Now()

		if err == nil {
			// Recovery: report the transition if we previously failed.
			if lastErr != nil {
				log.Infof("network sends recovered after %d failures", suppressed+1)
				lastErr = nil
				suppressed = 0
				hintShown = true // keep the hint from repeating
			}
			return
		}

		if lastErr != nil && now.Sub(lastReport) < reportInterval {
			suppressed++
			return
		}
		lastReport = now
		lastErr = err
		log.Warnf("send failed: %v (%d more suppressed)", err, suppressed)
		suppressed = 0
		if !hintShown {
			hintShown = true
			log.Warnf(
				"UDP datagrams cannot leave this machine. This is usually caused by " +
					"macOS Local Network privacy blocking the terminal app (System " +
					"Settings -> Privacy & Security -> Local Network), or by a VPN/" +
					"firewall. Unicast and broadcast traffic to the LAN are affected " +
					"as well. Loopback is not: local testing works with -interface lo0.")
		}
	}
}

// initSessionState prepares the initial identity and session state. It
// mirrors Controller construction + resetState in the official implementation.
func (e *engine) initSessionState() {
	e.nodeId = randomNodeId()
	e.sessionId = e.nodeId
	now := Now()
	e.sessionXForm = initXForm(now)
	e.sessionTimeline = clampTempo(Timeline{
		Tempo:      TempoFromBPM(e.cfg.tempoOrDefault()),
		BeatOrigin: Beats{0},
		TimeOrigin: 0,
	})
	e.sessionStartStop = StartStopState{}

	hostTime := e.sessionXForm.GhostToHost(0)
	e.clientTimeline = Timeline{
		Tempo:      e.sessionTimeline.Tempo,
		BeatOrigin: e.sessionTimeline.BeatOrigin,
		TimeOrigin: hostTime,
	}
	e.clientStartStop = clientStartStop{isPlaying: false, time: hostTime, timestamp: hostTime}
	e.lastPlayingCb = false
}

func (e *engine) run() {
	broadcastTicker := time.NewTicker(broadcastPeriod)
	pruneTicker := time.NewTicker(pruneInterval)
	defer broadcastTicker.Stop()
	defer pruneTicker.Stop()
	defer func() {
		if e.enabledFlag {
			e.sendByeBye()
		}
		e.hub.stopAll()
	}()

	for {
		select {
		case <-e.done:
			return
		case p := <-e.mcCh:
			e.handleDiscoveryPacket(p)
		case p := <-e.bcCh:
			e.handleDiscoveryPacket(p)
		case p := <-e.ucCh:
			e.handleDiscoveryPacket(p)
		case p := <-e.msCh:
			e.handleMeasurementPacket(p)
		case <-broadcastTicker.C:
			e.broadcast()
		case <-pruneTicker.C:
			e.prunePeers()
		case fn := <-e.cmdCh:
			fn()
		case id := <-e.timerC:
			e.hub.fire(id)
		}
	}
}

// ---------------------------------------------------------------------------
// Discovery
// ---------------------------------------------------------------------------

func (e *engine) currentPeerState() PeerState {
	return PeerState{
		NodeState: NodeState{
			NodeID:    e.nodeId,
			SessionID: e.sessionId,
			Timeline:  e.sessionTimeline,
			StartStop: e.sessionStartStop,
		},
		MeasurementEndpoint: e.net.measurementEndpoint(),
	}
}

func (e *engine) broadcast() {
	if !e.enabledFlag {
		return
	}
	now := time.Now()
	if elapsed := now.Sub(e.lastBroadcast); elapsed < minBroadcastGap {
		if !e.broadcastPending {
			e.broadcastPending = true
			e.broadcastTimerID = e.hub.after(minBroadcastGap-elapsed, func() {
				e.broadcastPending = false
				e.broadcastNow()
			})
		}
		return
	}
	e.broadcastNow()
}

func (e *engine) broadcastNow() {
	if !e.enabledFlag {
		return
	}
	e.lastBroadcast = time.Now()
	msg := encodeDiscoveryMessage(discoveryAlive, discoveryTTL, discoveryGroupID, e.nodeId, encodePeerStateEntries(e.currentPeerState()))
	e.net.sendMulticast(msg)
}

func (e *engine) sendResponseTo(from *net.UDPAddr) {
	msg := encodeDiscoveryMessage(discoveryResp, discoveryTTL, discoveryGroupID, e.nodeId, encodePeerStateEntries(e.currentPeerState()))
	e.net.sendTo(msg, from)
}

func (e *engine) sendByeBye() {
	msg := encodeDiscoveryMessage(discoveryByeBye, 0, discoveryGroupID, e.nodeId, nil)
	e.net.sendMulticast(msg)
}

func (e *engine) handleDiscoveryPacket(p udpPacket) {
	e.log.Debugf("discovery packet: %d bytes from %v", len(p.data), p.from)
	hdr, payload, ok := parseDiscoveryMessage(p.data)
	if !ok || hdr.groupID != discoveryGroupID || hdr.nodeID.eq(e.nodeId) {
		return
	}
	switch hdr.msgType {
	case discoveryAlive:
		if !e.enabledFlag {
			return
		}
		e.sawPeerState(hdr, payload, p.from)
		e.sendResponseTo(p.from)
	case discoveryResp:
		if !e.enabledFlag {
			return
		}
		e.sawPeerState(hdr, payload, p.from)
	case discoveryByeBye:
		e.removePeer(hdr.nodeID)
	}
}

func (e *engine) sawPeerState(hdr discoveryHeader, payload []byte, from *net.UDPAddr) {
	state, err := decodePeerState(hdr.nodeID, payload)
	if err != nil {
		e.log.Warnf("ignoring peer state from %v: %v", from, err)
		return
	}

	peer, existed := e.peers[hdr.nodeID]
	if !existed {
		peer = &peerEntry{}
		e.peers[hdr.nodeID] = peer
	}
	oldSession := peer.state.SessionID
	wasMember := existed && oldSession.eq(e.sessionId)

	peer.state = state
	peer.src = from
	now := time.Now()
	peer.lastSeen = now
	peer.expireAt = now.Add(peerTTLDuration + peerPrunePad)

	// Propagate timeline changes to the session logic (before start/stop,
	// matching the official implementation's ordering).
	if !peer.tlKnown || !peer.tlSession.eq(state.SessionID) || !peer.tlPropagated.eq(state.Timeline) {
		peer.tlKnown = true
		peer.tlSession = state.SessionID
		peer.tlPropagated = state.Timeline
		e.sawSessionTimeline(state.SessionID, state.Timeline)
	}

	// Then propagate start/stop state changes.
	if !peer.ssKnown || !peer.ssSession.eq(state.SessionID) || !peer.ssPropagated.eq(state.StartStop) {
		peer.ssKnown = true
		peer.ssSession = state.SessionID
		peer.ssPropagated = state.StartStop
		e.handleStartStopFromSession(state.SessionID, state.StartStop)
	}

	// Membership changed if the peer is new or changed session.
	if !existed || !oldSession.eq(state.SessionID) || wasMember != state.SessionID.eq(e.sessionId) {
		e.sessionMembershipChanged()
	}
}

func (e *engine) removePeer(id NodeId) {
	if _, ok := e.peers[id]; ok {
		delete(e.peers, id)
		e.sessionMembershipChanged()
	}
}

func (e *engine) prunePeers() {
	now := time.Now()
	expired := make([]NodeId, 0, 4)
	for id, peer := range e.peers {
		if now.After(peer.expireAt) {
			expired = append(expired, id)
		}
	}
	for _, id := range expired {
		e.log.Infof("peer %s timed out", id)
		delete(e.peers, id)
	}
	if len(expired) > 0 {
		e.sessionMembershipChanged()
	}
}

// sessionMembershipChanged recounts the peers in our session. When the count
// drops to zero we found a new session, mirroring the official behavior.
func (e *engine) sessionMembershipChanged() {
	count := 0
	for _, peer := range e.peers {
		if peer.state.SessionID.eq(e.sessionId) {
			count++
		}
	}
	if count != e.sessionPeerNbr {
		old := e.sessionPeerNbr
		e.sessionPeerNbr = count
		if count == 0 && old > 0 {
			e.resetState()
		}
		e.publishSnapshot()
		e.l.firePeersCallback(count)
	}
}

// ---------------------------------------------------------------------------
// Sessions
// ---------------------------------------------------------------------------

// sawSessionTimeline considers an observed (session, timeline) pair. For our
// current session we adopt the timeline if it has a higher beat origin; for
// other sessions we track them and measure to decide whether to join.
func (e *engine) sawSessionTimeline(sid NodeId, tl Timeline) {
	if sid.eq(e.sessionId) {
		if tl.BeatOrigin.greater(e.sessionTimeline.BeatOrigin) {
			e.log.Debugf("adopting peer timeline %.3f BPM (origin %.3f)", tl.Tempo.BPM(), tl.BeatOrigin.Float())
			e.updateSessionTiming(tl, e.sessionXForm)
		}
		e.broadcast()
		return
	}

	if s, ok := e.otherSessions[sid]; ok {
		if tl.BeatOrigin.greater(s.timeline.BeatOrigin) {
			s.timeline = tl
		}
		return
	}
	e.otherSessions[sid] = &sessionEntry{timeline: tl}
	e.launchSessionMeasurement(sid)
}

// launchSessionMeasurement starts a clock measurement against one of the
// peers of the given session, preferring the session's founding peer.
func (e *engine) launchSessionMeasurement(sid NodeId) {
	if _, inProgress := e.measurements[sid]; inProgress {
		return
	}
	var candidates []*peerEntry
	for _, peer := range e.peers {
		if peer.state.SessionID.eq(sid) {
			candidates = append(candidates, peer)
		}
	}
	if len(candidates) == 0 {
		delete(e.otherSessions, sid)
		return
	}
	sort.Slice(candidates, func(i, j int) bool {
		return candidates[i].state.NodeID.compare(candidates[j].state.NodeID) < 0
	})
	chosen := candidates[0]
	for _, c := range candidates {
		if c.state.NodeID.eq(sid) {
			chosen = c
			break
		}
	}
	if chosen.state.MeasurementEndpoint == nil {
		e.handleSessionMeasurementResult(sid, GhostXForm{})
		return
	}
	e.startMeasurement(sid, chosen.state.MeasurementEndpoint)
}

func (e *engine) scheduleRemeasure() {
	if e.remeasureID != 0 {
		e.hub.cancel(e.remeasureID)
	}
	e.remeasureID = e.hub.after(remeasureAfter, func() {
		e.remeasureID = 0
		e.launchSessionMeasurement(e.sessionId)
		e.scheduleRemeasure()
	})
}

// handleSessionMeasurementResult processes the outcome of a session clock
// measurement and decides whether to join the measured session.
func (e *engine) handleSessionMeasurementResult(sid NodeId, xform GhostXForm) {
	if sid.eq(e.sessionId) {
		// Re-measurement of the current session: refresh the clock offset.
		if !xform.eq(GhostXForm{}) {
			e.updateSessionTiming(e.sessionTimeline, xform)
			e.broadcast()
		}
		e.scheduleRemeasure()
		return
	}

	s, ok := e.otherSessions[sid]
	if !ok {
		return
	}
	if xform.eq(GhostXForm{}) {
		// Measurement failed; forget the session (it will be re-measured if
		// seen again).
		delete(e.otherSessions, sid)
		return
	}

	now := Now()
	curGhost := e.sessionXForm.HostToGhost(now)
	newGhost := xform.HostToGhost(now)
	ghostDiff := newGhost - curGhost

	if ghostDiff > sessionEpsilon ||
		(abs64(int64(ghostDiff)) < int64(sessionEpsilon) && sid.compare(e.sessionId) < 0) {
		// The other session wins: switch over to it.
		e.log.Infof("joining session %s (tempo %.2f BPM)", sid, s.timeline.Tempo.BPM())
		old := &sessionEntry{timeline: e.sessionTimeline}
		delete(e.otherSessions, sid)
		if _, exists := e.otherSessions[e.sessionId]; !exists {
			e.otherSessions[e.sessionId] = old
		}
		e.sessionId = sid
		e.sessionStartStop = StartStopState{}
		e.updateSessionTiming(s.timeline, xform)
		e.broadcast()
		e.sessionMembershipChanged()
		e.scheduleRemeasure()
	}
}

// resetState founds a new session, keeping the client timeline continuous.
func (e *engine) resetState() {
	e.nodeId = randomNodeId()
	e.sessionId = e.nodeId
	now := Now()
	xform := initXForm(now)
	hostTime := xform.GhostToHost(0)

	// Make the new timeline continuous by finding the beat corresponding to
	// the current host time under the old mapping and anchoring it in the new
	// ghost time domain.
	newTl := Timeline{
		Tempo:      e.sessionTimeline.Tempo,
		BeatOrigin: e.sessionTimeline.ToBeats(e.sessionXForm.HostToGhost(hostTime)),
		TimeOrigin: xform.HostToGhost(hostTime),
	}

	e.sessionStartStop = StartStopState{}
	e.updateSessionTiming(newTl, xform)
	e.broadcast()

	e.otherSessions = make(map[NodeId]*sessionEntry)
	e.peers = make(map[NodeId]*peerEntry)
	e.log.Infof("founded new session %s", e.sessionId)
}

// updateSessionTiming applies a new session timeline and/or clock transform,
// keeping the client timeline continuous and the transport state mapped.
func (e *engine) updateSessionTiming(newTimeline Timeline, newXForm GhostXForm) {
	newTimeline = clampTempo(newTimeline)
	oldTimeline := e.sessionTimeline
	oldXForm := e.sessionXForm
	if oldTimeline.eq(newTimeline) && oldXForm.eq(newXForm) {
		return
	}

	e.sessionTimeline = newTimeline
	e.sessionXForm = newXForm

	now := Now()
	e.clientTimeline = updateClientTimelineFromSession(e.clientTimeline, newTimeline, now, newXForm)

	if e.startStopSync && !e.sessionStartStop.isEmpty() {
		e.clientStartStop = startStopSessionToClient(e.sessionStartStop, newTimeline, newXForm)
		e.reportPlayingIfChanged()
	}

	if !oldTimeline.Tempo.eq(newTimeline.Tempo) {
		e.l.fireTempoCallback(newTimeline.Tempo.BPM())
	}
	e.publishSnapshot()
}

func (e *engine) handleStartStopFromSession(sid NodeId, ss StartStopState) {
	if !sid.eq(e.sessionId) || ss.Timestamp <= e.sessionStartStop.Timestamp {
		return
	}
	e.sessionStartStop = ss
	e.broadcast() // Always relay, even with start/stop sync disabled.

	if e.startStopSync && !ss.isEmpty() {
		e.clientStartStop = startStopSessionToClient(ss, e.sessionTimeline, e.sessionXForm)
		e.reportPlayingIfChanged()
		e.publishSnapshot()
	}
}

func (e *engine) reportPlayingIfChanged() {
	if e.clientStartStop.isPlaying != e.lastPlayingCb {
		e.lastPlayingCb = e.clientStartStop.isPlaying
		e.l.fireStartStopCallback(e.clientStartStop.isPlaying)
	}
}

// ---------------------------------------------------------------------------
// Client state changes (invoked via commands from the public API)
// ---------------------------------------------------------------------------

// applyTempoChange mirrors BasicLink::SessionState::setTempo + commit.
func (e *engine) applyTempoChange(bpm float64, at Micros) {
	desired := clampTempo(Timeline{
		Tempo:      TempoFromBPM(bpm),
		BeatOrigin: e.clientTimeline.ToBeats(at),
		TimeOrigin: at,
	})
	e.clientTimeline.Tempo = desired.Tempo
	e.clientTimeline.TimeOrigin = desired.FromBeats(e.clientTimeline.BeatOrigin)
	e.commitClientTimeline(at)
}

func (e *engine) commitClientTimeline(at Micros) {
	stl := updateSessionTimelineFromClient(e.sessionTimeline, e.clientTimeline, at, e.sessionXForm)
	e.updateSessionTiming(stl, e.sessionXForm)
	// Optimistically cache the new timeline for all peers of our session.
	for _, peer := range e.peers {
		if peer.state.SessionID.eq(e.sessionId) {
			peer.state.Timeline = e.sessionTimeline
			peer.tlPropagated = e.sessionTimeline
		}
	}
	e.broadcast()
}

func (e *engine) applyPlayingChange(playing bool, at Micros) {
	// Prevent updating with an outdated state.
	if at >= e.clientStartStop.timestamp {
		e.clientStartStop = clientStartStop{isPlaying: playing, time: at, timestamp: at}
	}

	if e.startStopSync {
		ghostTs := e.sessionXForm.HostToGhost(at)
		if ghostTs > e.sessionStartStop.Timestamp {
			e.sessionStartStop = StartStopState{
				IsPlaying: playing,
				Beats:     e.sessionTimeline.ToBeats(e.sessionXForm.HostToGhost(at)),
				Timestamp: ghostTs,
			}
			e.broadcast()
		}
	}
	e.reportPlayingIfChanged()
	e.publishSnapshot()
}

// applyRequestBeatAtTime mirrors BasicLink::SessionState::requestBeatAtTime.
func (e *engine) applyRequestBeatAtTime(beat float64, at Micros, quantum Beats) {
	if e.sessionPeerNbr > 0 {
		// Respect the quantum when connected to other peers.
		next := NextPhaseMatch(
			ToPhaseEncodedBeats(e.clientTimeline, at, quantum),
			BeatsFromFloat(beat),
			quantum)
		at = FromPhaseEncodedBeats(e.clientTimeline, next, quantum)
	}
	e.applyForceBeatAtTime(beat, at, quantum)
}

// applyForceBeatAtTime mirrors BasicLink::SessionState::forceBeatAtTime.
func (e *engine) applyForceBeatAtTime(beat float64, at Micros, quantum Beats) {
	beatB := BeatsFromFloat(beat)
	forceBeatAtTimeImpl(&e.clientTimeline, beatB, at, quantum)
	// Due to quantization errors the resulting beat at 'at' can be bigger
	// than the requested beat; shift the timeline forwards to compensate.
	if ToPhaseEncodedBeats(e.clientTimeline, at, quantum).greater(beatB) {
		forceBeatAtTimeImpl(&e.clientTimeline, beatB, at+1, quantum)
	}
	e.commitClientTimeline(at)
}

// startStopSessionToClient maps a session start/stop state to client state.
func startStopSessionToClient(ss StartStopState, tl Timeline, xform GhostXForm) clientStartStop {
	return clientStartStop{
		isPlaying: ss.IsPlaying,
		time:      xform.GhostToHost(tl.FromBeats(ss.Beats)),
		timestamp: xform.GhostToHost(ss.Timestamp),
	}
}

// ---------------------------------------------------------------------------
// Enable/disable
// ---------------------------------------------------------------------------

func (e *engine) setEnabled(v bool) {
	if v == e.enabledFlag {
		return
	}
	e.enabledFlag = v
	if v {
		// Always reset when first enabling to avoid hijacking the tempo of
		// existing sessions.
		e.resetState()
		e.broadcast()
	} else {
		e.sendByeBye()
		e.peers = make(map[NodeId]*peerEntry)
		e.otherSessions = make(map[NodeId]*sessionEntry)
		e.sessionPeerNbr = 0
		e.publishSnapshot()
	}
}

// ---------------------------------------------------------------------------
// Snapshot & callbacks
// ---------------------------------------------------------------------------

func (e *engine) publishSnapshot() {
	peers := make([]PeerInfo, 0, len(e.peers))
	for _, peer := range e.peers {
		var src *net.UDPAddr
		if peer.src != nil {
			src = peer.src
		} else {
			src = peer.state.MeasurementEndpoint
		}
		peers = append(peers, PeerInfo{
			NodeID:    peer.state.NodeID,
			SessionID: peer.state.SessionID,
			Tempo:     peer.state.Timeline.Tempo.BPM(),
			Addr:      src,
		})
	}
	sort.Slice(peers, func(i, j int) bool {
		return peers[i].NodeID.compare(peers[j].NodeID) < 0
	})
	e.l.setSnapshot(snapshot{
		enabled:   e.enabledFlag,
		numPeers:  e.sessionPeerNbr,
		tempo:     e.sessionTimeline.Tempo.BPM(),
		playing:   e.clientStartStop.isPlaying,
		timeline:  e.clientTimeline,
		nodeId:    e.nodeId,
		sessionId: e.sessionId,
		peers:     peers,
	})
}

// ---------------------------------------------------------------------------
// Small helpers
// ---------------------------------------------------------------------------

func median(xs []float64) float64 {
	sort.Float64s(xs)
	n := len(xs)
	if n == 0 {
		return 0
	}
	if n%2 == 0 {
		return (xs[n/2] + xs[(n-1)/2]) / 2
	}
	return xs[n/2]
}

func (c Config) tempoOrDefault() float64 {
	if c.Tempo <= 0 {
		return 120
	}
	return c.Tempo
}
