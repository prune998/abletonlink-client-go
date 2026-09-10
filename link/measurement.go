package link

import (
	"net"
)

// This file implements the Link ping/pong clock measurement protocol
// (_link_v1 messages on the measurement socket), mirroring
// ableton::link::PingResponder and ableton::link::Measurement.
//
// A responder replies to every ping with a pong carrying its current session
// id, its current ghost time, and the raw ping payload echoed back. A
// measurer exchanges pings/pongs with a peer to estimate the offset between
// the peer's ghost clock and the local clock, using the median of >100
// individual estimates for robustness against network jitter.

// measurer measures the clock offset to a remote peer's ghost time domain.
type measurer struct {
	sessionID NodeId // expected session of the remote peer (and map key)
	target    *net.UDPAddr

	points    []float64
	started   int
	lastHost  Micros // host time of the last ping we sent
	lastGhost Micros // ghost time reported in the last pong
	timerID   uint64
}

// startMeasurement begins measuring the given peer's measurement endpoint.
func (e *engine) startMeasurement(sid NodeId, target *net.UDPAddr) {
	m := &measurer{sessionID: sid, target: target}
	e.measurements[sid] = m
	e.sendPing(m, Now(), 0)
	m.timerID = e.hub.after(measureTimer, func() { e.measurerTimerFired(sid) })
	e.log.Debugf("measuring peer of session %s at %v", sid, target)
}

func (e *engine) sendPing(m *measurer, host Micros, prevGhost Micros) {
	entries := []entry{encodeMicrosEntry(keyHostTime, host)}
	if prevGhost != 0 {
		entries = append(entries, encodeMicrosEntry(keyPrevGHostT, prevGhost))
	}
	msg := encodeLinkMessage(linkPing, entries)
	m.lastHost = host
	e.net.sendMeasurement(msg, m.target)
}

func (e *engine) handleMeasurementPacket(p udpPacket) {
	if !e.enabledFlag {
		return
	}
	msgType, payload, ok := parseLinkMessage(p.data)
	if !ok {
		return
	}
	switch msgType {
	case linkPing:
		e.handlePing(p.from, payload)
	case linkPong:
		e.handlePong(p.from, payload)
	}
}

// handlePing replies to a ping with a pong carrying the current session id
// and ghost time, echoing the ping payload back.
func (e *engine) handlePing(from *net.UDPAddr, pingPayload []byte) {
	now := Now()
	ghostTime := e.sessionXForm.HostToGhost(now)
	entries := []entry{
		{keySession, append([]byte(nil), e.sessionId[:]...)},
		encodeMicrosEntry(keyGHostTime, ghostTime),
	}
	msg := encodeLinkMessage(linkPong, entries)
	msg = append(msg, pingPayload...)
	e.net.sendMeasurement(msg, from)
}

// handlePong processes a pong for one of our in-flight measurements.
func (e *engine) handlePong(from *net.UDPAddr, payload []byte) {
	var m *measurer
	var key NodeId
	for sid, meas := range e.measurements {
		if meas.target.IP.Equal(from.IP) && meas.target.Port == from.Port {
			m = meas
			key = sid
			break
		}
	}
	if m == nil {
		return
	}

	var (
		sessID    NodeId
		ghostTime Micros
		prevGhost Micros
		prevHost  Micros
		hasPrevG  bool
		hasPrevH  bool
		parseErr  error
	)
	parseErr = parseEntries(payload, func(k uint32, v []byte) error {
		switch k {
		case keySession:
			if len(v) != 8 {
				return errBadEntry
			}
			copy(sessID[:], v)
		case keyGHostTime:
			var e error
			ghostTime, e = decodeMicros(v)
			return e
		case keyPrevGHostT:
			var e error
			prevGhost, e = decodeMicros(v)
			hasPrevG = e == nil
			return e
		case keyHostTime:
			var e error
			prevHost, e = decodeMicros(v)
			hasPrevH = e == nil
			return e
		}
		return nil
	})
	if parseErr != nil {
		e.failMeasurement(key)
		return
	}

	if !sessID.eq(m.sessionID) {
		// The peer switched sessions while we were measuring: give up.
		e.failMeasurement(key)
		return
	}

	now := Now()
	// Keep the dialog going with a new ping echoing the peer's ghost time.
	e.sendPing(m, now, ghostTime)

	if ghostTime != 0 && hasPrevH && prevHost != 0 {
		m.points = append(m.points,
			float64(ghostTime)-(float64(now)+float64(prevHost))*0.5)
	}
	if hasPrevG && prevGhost != 0 {
		m.points = append(m.points,
			(float64(ghostTime)+float64(prevGhost))*0.5-float64(prevHost))
	}

	if len(m.points) > maxMeasurePoints {
		e.finishMeasurement(key, GhostXForm{Slope: 1, Intercept: Micros(llround(median(m.points)))})
		return
	}

	// Re-arm the watchdog.
	e.hub.cancel(m.timerID)
	m.timerID = e.hub.after(measureTimer, func() { e.measurerTimerFired(key) })
}

func (e *engine) measurerTimerFired(key NodeId) {
	m, ok := e.measurements[key]
	if !ok {
		return
	}
	if m.started < maxMeasureStarts {
		e.sendPing(m, Now(), 0)
		m.started++
		m.timerID = e.hub.after(measureTimer, func() { e.measurerTimerFired(key) })
		return
	}
	e.failMeasurement(key)
}

func (e *engine) finishMeasurement(key NodeId, xform GhostXForm) {
	if m, ok := e.measurements[key]; ok {
		e.hub.cancel(m.timerID)
		delete(e.measurements, key)
	}
	e.log.Debugf("measurement of session %s finished: slope=%f intercept=%d", key, xform.Slope, xform.Intercept)
	e.handleSessionMeasurementResult(key, xform)
}

func (e *engine) failMeasurement(key NodeId) {
	if m, ok := e.measurements[key]; ok {
		e.hub.cancel(m.timerID)
		delete(e.measurements, key)
	}
	e.log.Debugf("measurement of session %s failed", key)
	e.handleSessionMeasurementResult(key, GhostXForm{})
}

type entryError struct{}

func (entryError) Error() string { return "link: bad entry" }

var errBadEntry error = entryError{}
