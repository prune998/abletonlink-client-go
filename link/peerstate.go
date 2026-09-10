package link

import (
	"encoding/binary"
	"errors"
	"net"
)

// StartStopState is the transport state of a session: whether it is playing,
// at which beat position the state changed and when (in ghost time). It
// mirrors ableton::link::StartStopState. The zero value means "no state".
type StartStopState struct {
	IsPlaying bool
	Beats     Beats
	Timestamp Micros
}

func (s StartStopState) eq(o StartStopState) bool {
	return s.IsPlaying == o.IsPlaying && s.Beats.eq(o.Beats) && s.Timestamp == o.Timestamp
}

func (s StartStopState) isEmpty() bool {
	return s.eq(StartStopState{})
}

// encodeStartStopEntry encodes the 'stst' entry value: isPlaying byte, beats
// int64, timestamp int64.
func encodeStartStopEntry(s StartStopState) entry {
	v := make([]byte, 0, 17)
	if s.IsPlaying {
		v = append(v, 1)
	} else {
		v = append(v, 0)
	}
	v = binary.BigEndian.AppendUint64(v, uint64(s.Beats.MicroBeats()))
	v = binary.BigEndian.AppendUint64(v, uint64(int64(s.Timestamp)))
	return entry{keyStartStop, v}
}

func decodeStartStopEntry(value []byte) (StartStopState, error) {
	var s StartStopState
	if len(value) != 17 {
		return s, errors.New("link: bad start/stop entry size")
	}
	s.IsPlaying = value[0] != 0
	s.Beats = BeatsFromMicro(int64(binary.BigEndian.Uint64(value[1:])))
	s.Timestamp = Micros(int64(binary.BigEndian.Uint64(value[9:])))
	return s, nil
}

// encodeTimelineEntry encodes the 'tmln' entry value: microsPerBeat int64,
// beatOrigin (microbeats) int64, timeOrigin int64.
func encodeTimelineEntry(tl Timeline) entry {
	v := make([]byte, 0, 24)
	v = binary.BigEndian.AppendUint64(v, uint64(tl.Tempo.MicrosPerBeat()))
	v = binary.BigEndian.AppendUint64(v, uint64(tl.BeatOrigin.MicroBeats()))
	v = binary.BigEndian.AppendUint64(v, uint64(int64(tl.TimeOrigin)))
	return entry{keyTimeline, v}
}

func decodeTimelineEntry(value []byte) (Timeline, error) {
	var tl Timeline
	if len(value) != 24 {
		return tl, errors.New("link: bad timeline entry size")
	}
	microsPerBeat := int64(binary.BigEndian.Uint64(value))
	if microsPerBeat <= 0 {
		return tl, errors.New("link: invalid micros per beat")
	}
	tl.Tempo = TempoFromBPM(60 * 1e6 / float64(microsPerBeat))
	tl.BeatOrigin = BeatsFromMicro(int64(binary.BigEndian.Uint64(value[8:])))
	tl.TimeOrigin = Micros(int64(binary.BigEndian.Uint64(value[16:])))
	return tl, nil
}

// NodeState is the node's own state: identity, session membership, timeline
// and transport state.
type NodeState struct {
	NodeID    NodeId
	SessionID NodeId
	Timeline  Timeline
	StartStop StartStopState
}

// PeerState is the state of a remote peer as advertised via discovery. It
// carries the peer's NodeState plus the endpoint of its ping/pong measurement
// server. It mirrors ableton::link::PeerState (IPv4 only).
type PeerState struct {
	NodeState
	MeasurementEndpoint *net.UDPAddr
}

// encodePeerStateEntries builds the payload entries for a peer state. The
// order matches the official implementation: timeline, session membership,
// start/stop state, then the measurement endpoint.
func encodePeerStateEntries(state PeerState) []entry {
	entries := []entry{
		encodeTimelineEntry(state.Timeline),
		{keySession, append([]byte(nil), state.SessionID[:]...)},
		encodeStartStopEntry(state.StartStop),
	}
	if ep := state.MeasurementEndpoint; ep != nil && ep.IP.To4() != nil {
		v := make([]byte, 0, 6)
		v = append(v, ep.IP.To4()...)
		v = binary.BigEndian.AppendUint16(v, uint16(ep.Port))
		entries = append(entries, entry{keyMeepV4, v})
	}
	return entries
}

// decodePeerState parses a peer state payload.
func decodePeerState(id NodeId, payload []byte) (PeerState, error) {
	state := PeerState{NodeState: NodeState{NodeID: id}}
	err := parseEntries(payload, func(key uint32, value []byte) error {
		switch key {
		case keyTimeline:
			tl, err := decodeTimelineEntry(value)
			if err != nil {
				return err
			}
			state.Timeline = tl
		case keySession:
			if len(value) != 8 {
				return errors.New("link: bad session entry size")
			}
			copy(state.SessionID[:], value)
		case keyStartStop:
			ss, err := decodeStartStopEntry(value)
			if err != nil {
				return err
			}
			state.StartStop = ss
		case keyMeepV4:
			if len(value) != 6 {
				return errors.New("link: bad measurement endpoint entry size")
			}
			ip := make(net.IP, 4)
			copy(ip, value[:4])
			state.MeasurementEndpoint = &net.UDPAddr{
				IP:   ip,
				Port: int(binary.BigEndian.Uint16(value[4:])),
			}
		default:
			// Unknown entry: ignore for forward compatibility.
		}
		return nil
	})
	if err != nil {
		return PeerState{}, err
	}
	return state, nil
}
