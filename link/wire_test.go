package link

import (
	"bytes"
	"encoding/binary"
	"net"
	"testing"
)

func TestEncodeTimelineEntryLayout(t *testing.T) {
	// tempo 120 BPM -> 500000 micros/beat; beat origin 1.5 -> 1500000
	// micro-beats; time origin 1234.
	tl := Timeline{
		Tempo:      TempoFromBPM(120),
		BeatOrigin: BeatsFromFloat(1.5),
		TimeOrigin: 1234,
	}
	e := encodeTimelineEntry(tl)

	if e.key != 0x746d6c6e {
		t.Fatalf("timeline key = %#x, want 0x746d6c6e ('tmln')", e.key)
	}
	if len(e.value) != 24 {
		t.Fatalf("timeline value size = %d, want 24", len(e.value))
	}

	want := []byte{
		// key 'tmln'
		0x74, 0x6d, 0x6c, 0x6e,
		// size 24
		0x00, 0x00, 0x00, 0x18,
		// micros per beat = 500000
		0x00, 0x00, 0x00, 0x00, 0x00, 0x07, 0xa1, 0x20,
		// beat origin = 1500000 micro-beats
		0x00, 0x00, 0x00, 0x00, 0x00, 0x16, 0xe3, 0x60,
		// time origin = 1234
		0x00, 0x00, 0x00, 0x00, 0x00, 0x00, 0x04, 0xd2,
	}
	// As encoded within a payload: key + size + value.
	got := encodeEntries(nil, []entry{e})
	if !bytes.Equal(got, want) {
		t.Fatalf("timeline entry mismatch:\n got %x\nwant %x", got, want)
	}

	// Roundtrip.
	tl2, err := decodeTimelineEntry(e.value)
	if err != nil {
		t.Fatal(err)
	}
	if !tl2.eq(tl) {
		t.Fatalf("roundtrip mismatch: %+v != %+v", tl2, tl)
	}
}

func TestStartStopEntryRoundtrip(t *testing.T) {
	ss := StartStopState{IsPlaying: true, Beats: BeatsFromFloat(-3.25), Timestamp: -98765}
	e := encodeStartStopEntry(ss)
	if len(e.value) != 17 {
		t.Fatalf("start/stop value size = %d, want 17", len(e.value))
	}
	if e.key != 0x73747374 {
		t.Fatalf("start/stop key = %#x, want 0x73747374 ('stst')", e.key)
	}
	got, err := decodeStartStopEntry(e.value)
	if err != nil {
		t.Fatal(err)
	}
	if !got.eq(ss) {
		t.Fatalf("roundtrip mismatch: %+v != %+v", got, ss)
	}
}

func TestDiscoveryMessageRoundtrip(t *testing.T) {
	id := NodeId{1, 2, 3, 4, 5, 6, 7, 8}
	entries := encodePeerStateEntries(PeerState{
		NodeState: NodeState{
			NodeID:    id,
			SessionID: NodeId{9, 9, 9, 9, 9, 9, 9, 9},
			Timeline: Timeline{
				Tempo:      TempoFromBPM(98.5),
				BeatOrigin: BeatsFromFloat(12.25),
				TimeOrigin: 77777,
			},
			StartStop: StartStopState{IsPlaying: true, Beats: BeatsFromFloat(4), Timestamp: 4242},
		},
		MeasurementEndpoint: &net.UDPAddr{IP: net.IPv4(192, 168, 1, 42).To4(), Port: 51234},
	})
	msg := encodeDiscoveryMessage(discoveryAlive, discoveryTTL, discoveryGroupID, id, entries)

	hdr, payload, ok := parseDiscoveryMessage(msg)
	if !ok {
		t.Fatal("failed to parse our own discovery message")
	}
	if hdr.msgType != discoveryAlive || hdr.ttl != discoveryTTL || hdr.groupID != 0 {
		t.Fatalf("bad header: %+v", hdr)
	}
	if !hdr.nodeID.eq(id) {
		t.Fatalf("bad node id: %s", hdr.nodeID)
	}

	state, err := decodePeerState(hdr.nodeID, payload)
	if err != nil {
		t.Fatal(err)
	}
	if !state.SessionID.eq(NodeId{9, 9, 9, 9, 9, 9, 9, 9}) {
		t.Fatalf("bad session id: %s", state.SessionID)
	}
	if got := state.Timeline.Tempo.MicrosPerBeat(); got != TempoFromBPM(98.5).MicrosPerBeat() {
		t.Fatalf("bad tempo micros/beat: %d", got)
	}
	if !state.StartStop.IsPlaying {
		t.Fatal("start/stop roundtrip failed")
	}
	if state.MeasurementEndpoint == nil || state.MeasurementEndpoint.Port != 51234 {
		t.Fatalf("bad measurement endpoint: %v", state.MeasurementEndpoint)
	}
	if ip := state.MeasurementEndpoint.IP.To4(); ip == nil || ip[3] != 42 {
		t.Fatalf("bad measurement endpoint IP: %v", state.MeasurementEndpoint.IP)
	}
}

func TestDiscoveryMessageUnknownEntriesIgnored(t *testing.T) {
	id := NodeId{1, 1, 1, 1, 1, 1, 1, 1}
	msg := encodeDiscoveryMessage(discoveryAlive, discoveryTTL, 0, id, []entry{
		{key: 0xdeadbeef, value: []byte{1, 2, 3}},
		encodeTimelineEntry(Timeline{Tempo: TempoFromBPM(100), BeatOrigin: Beats{5}, TimeOrigin: 6}),
	})
	hdr, payload, ok := parseDiscoveryMessage(msg)
	if !ok {
		t.Fatal("parse failed")
	}
	state, err := decodePeerState(hdr.nodeID, payload)
	if err != nil {
		t.Fatalf("unknown entries must be ignored: %v", err)
	}
	if state.Timeline.Tempo.BPM() <= 0 {
		t.Fatal("timeline not parsed")
	}
}

func TestByeByeMessage(t *testing.T) {
	id := NodeId{7, 7, 7, 7, 7, 7, 7, 7}
	msg := encodeDiscoveryMessage(discoveryByeBye, 0, 0, id, nil)
	hdr, payload, ok := parseDiscoveryMessage(msg)
	if !ok {
		t.Fatal("parse failed")
	}
	if hdr.msgType != discoveryByeBye || len(payload) != 0 {
		t.Fatalf("bad byebye message: %+v payload=%d", hdr, len(payload))
	}
}

func TestLinkPingPongLayout(t *testing.T) {
	msg := encodeLinkMessage(linkPing, []entry{encodeMicrosEntry(keyHostTime, 123456)})
	typ, payload, ok := parseLinkMessage(msg)
	if !ok || typ != linkPing {
		t.Fatalf("bad ping parse: %v %d", ok, typ)
	}
	if !bytes.HasPrefix(msg, []byte("_link_v\x01")) {
		t.Fatal("missing _link_v magic")
	}
	var v int64
	buf := bytes.NewReader(payload[8:]) // skip entry header
	if err := binary.Read(buf, binary.BigEndian, &v); err != nil {
		t.Fatal(err)
	}
	if v != 123456 {
		t.Fatalf("host time = %d, want 123456", v)
	}
}
