package link

// Timeline is a bijection between beats and time given a valid tempo,
// mirroring ableton::link::Timeline. It doubles as a protocol payload entry.
type Timeline struct {
	Tempo      Tempo
	BeatOrigin Beats
	TimeOrigin Micros
}

// ToBeats maps a time to a beat position.
func (tl Timeline) ToBeats(t Micros) Beats {
	return tl.BeatOrigin.add(tl.Tempo.MicrosToBeats(t - tl.TimeOrigin))
}

// FromBeats maps a beat position to a time.
func (tl Timeline) FromBeats(b Beats) Micros {
	return tl.TimeOrigin + tl.Tempo.BeatsToMicros(b.sub(tl.BeatOrigin))
}

func (tl Timeline) eq(o Timeline) bool {
	return tl.Tempo.eq(o.Tempo) && tl.BeatOrigin.eq(o.BeatOrigin) && tl.TimeOrigin == o.TimeOrigin
}

// Tempo range clamps, matching the official Link implementation.
const (
	minBPM = 20.0
	maxBPM = 999.0
)

func clampTempo(tl Timeline) Timeline {
	bpm := tl.Tempo.bpm
	if bpm < minBPM {
		bpm = minBPM
	} else if bpm > maxBPM {
		bpm = maxBPM
	}
	return Timeline{Tempo{bpm}, tl.BeatOrigin, tl.TimeOrigin}
}

// ToPhaseEncodedBeats interprets the timeline as encoding a quantum boundary
// at its origin and returns a phase-encoded beat value relative to the given
// quantum for the given time. Mirrors ableton::link::toPhaseEncodedBeats.
func ToPhaseEncodedBeats(tl Timeline, t Micros, quantum Beats) Beats {
	beat := tl.ToBeats(t)
	return ClosestPhaseMatch(beat, beat.sub(tl.BeatOrigin), quantum)
}

// FromPhaseEncodedBeats is the inverse of ToPhaseEncodedBeats: given a phase
// encoded beat value, find the time value it maps to.
func FromPhaseEncodedBeats(tl Timeline, beat Beats, quantum Beats) Micros {
	fromOrigin := beat.sub(tl.BeatOrigin)
	originOffset := fromOrigin.sub(Phase(fromOrigin, quantum))
	// Invert the phase calculation so that it always rounds up in the middle
	// instead of down like ClosestPhaseMatch. Otherwise we end up rounding
	// down twice when a value is at phase quantum/2.
	inversePhaseOffset := ClosestPhaseMatch(
		quantum.sub(Phase(fromOrigin, quantum)),
		quantum.sub(Phase(beat, quantum)),
		quantum)
	return tl.FromBeats(tl.BeatOrigin.add(originOffset).add(quantum).sub(inversePhaseOffset))
}

// shiftClientTimeline shifts the timeline so that
// result.ToBeats(t) == client.ToBeats(t) + shift, preserving the invariant
// that timeOrigin corresponds to beat 0 on the session timeline.
func shiftClientTimeline(client Timeline, shift Beats) Timeline {
	timeDelta := client.FromBeats(shift) - client.FromBeats(Beats{0})
	client.TimeOrigin -= timeDelta
	return client
}

// updateClientTimelineFromSession returns a new version of the client
// timeline that is continuous with the previous one
// (curClient.ToBeats(atTime) == result.ToBeats(atTime)) while adopting the
// session tempo and anchoring the quantization grid to the session's beat 0.
func updateClientTimelineFromSession(curClient, session Timeline, atTime Micros, xform GhostXForm) Timeline {
	tempTl := Timeline{session.Tempo, curClient.ToBeats(atTime), atTime}
	hostBeatZero := xform.GhostToHost(session.FromBeats(Beats{0}))
	return Timeline{tempTl.Tempo, tempTl.ToBeats(hostBeatZero), hostBeatZero}
}

// updateSessionTimelineFromClient maps a client timeline change onto the
// session timeline. The resulting beat origin is guaranteed to be strictly
// greater than the current one so that peers will adopt the change.
func updateSessionTimelineFromClient(curSession, client Timeline, atTime Micros, xform GhostXForm) Timeline {
	ghostBeat0 := xform.HostToGhost(client.TimeOrigin)

	zero := Beats{0}
	if curSession.ToBeats(ghostBeat0).eq(zero) && client.Tempo.eq(curSession.Tempo) {
		return curSession
	}

	tempTl := Timeline{client.Tempo, zero, ghostBeat0}
	newBeatOrigin := curSession.ToBeats(xform.HostToGhost(atTime))
	if minOrigin := curSession.BeatOrigin.add(Beats{1}); newBeatOrigin.less(minOrigin) {
		newBeatOrigin = minOrigin
	}
	return Timeline{client.Tempo, newBeatOrigin, tempTl.FromBeats(newBeatOrigin)}
}

// forceBeatAtTimeImpl applies a phase shift and beat magnitude adjustment to
// the timeline so that the beat at time matches the given beat value.
func forceBeatAtTimeImpl(tl *Timeline, beat Beats, t Micros, quantum Beats) {
	curBeatAtTime := ToPhaseEncodedBeats(*tl, t, quantum)
	closestInPhase := ClosestPhaseMatch(curBeatAtTime, beat, quantum)
	*tl = shiftClientTimeline(*tl, closestInPhase.sub(curBeatAtTime))
	tl.BeatOrigin = tl.BeatOrigin.add(beat).sub(closestInPhase)
}
