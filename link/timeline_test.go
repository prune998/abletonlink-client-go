package link

import (
	"math"
	"testing"
)

func beatsAlmostEqual(a, b Beats) bool {
	return a.micro == b.micro
}

func TestPhasePositive(t *testing.T) {
	if got := Phase(BeatsFromFloat(5.25), BeatsFromFloat(4)); !beatsAlmostEqual(got, BeatsFromFloat(1.25)) {
		t.Fatalf("phase(5.25, 4) = %v, want 1.25", got.Float())
	}
	if got := Phase(BeatsFromFloat(8), BeatsFromFloat(4)); !beatsAlmostEqual(got, Beats{0}) {
		t.Fatalf("phase(8, 4) = %v, want 0", got.Float())
	}
}

func TestPhaseNegative(t *testing.T) {
	cases := []struct {
		beats, quantum, want float64
	}{
		{-1, 4, 3},
		{-5, 4, 3},
		{-0.5, 4, 3.5},
		{-4, 4, 0},
		{1.5, 0, 0},
	}
	for _, c := range cases {
		got := Phase(BeatsFromFloat(c.beats), BeatsFromFloat(c.quantum)).Float()
		if math.Abs(got-c.want) > 1e-9 {
			t.Errorf("phase(%v, %v) = %v, want %v", c.beats, c.quantum, got, c.want)
		}
	}
}

func TestNextAndClosestPhaseMatch(t *testing.T) {
	q := BeatsFromFloat(4)
	// next phase match must be > x and match target's phase
	next := NextPhaseMatch(BeatsFromFloat(2), BeatsFromFloat(7), q)
	if next.Float() <= 2 || math.Abs(Phase(next, q).Float()-Phase(BeatsFromFloat(7), q).Float()) > 1e-9 {
		t.Fatalf("nextPhaseMatch(2, 7, 4) = %v", next.Float())
	}
	// closest match must be within quantum/2
	closest := ClosestPhaseMatch(BeatsFromFloat(2), BeatsFromFloat(7), q)
	if math.Abs(closest.Float()-2) > 2+1e-9 {
		t.Fatalf("closestPhaseMatch too far: %v", closest.Float())
	}
	if math.Abs(Phase(closest, q).Float()-Phase(BeatsFromFloat(7), q).Float()) > 1e-9 {
		t.Fatalf("closestPhaseMatch phase mismatch: %v", closest.Float())
	}
}

func TestTempoConversions(t *testing.T) {
	tpb := TempoFromBPM(120)
	if got := tpb.MicrosPerBeat(); got != 500000 {
		t.Fatalf("microsPerBeat(120) = %d, want 500000", got)
	}
	b := tpb.MicrosToBeats(1000000)
	if math.Abs(b.Float()-2) > 1e-9 {
		t.Fatalf("microsToBeats(1s @120) = %v, want 2", b.Float())
	}
	m := tpb.BeatsToMicros(BeatsFromFloat(3))
	if m != 1500000 {
		t.Fatalf("beatsToMicros(3 @120) = %d, want 1500000", m)
	}
	// BPM roundtrip through micros/beat loses a bit of precision; check the
	// 999 BPM clamp behavior mentioned in the C++ sources.
	if got := TempoFromBPM(999).MicrosPerBeat(); got != 60060 {
		t.Fatalf("microsPerBeat(999) = %d, want 60060", got)
	}
}

func TestTimelineMapping(t *testing.T) {
	tl := Timeline{Tempo: TempoFromBPM(120), BeatOrigin: BeatsFromFloat(10), TimeOrigin: 1000000}
	if got := tl.ToBeats(2000000).Float(); math.Abs(got-12) > 1e-9 {
		t.Fatalf("toBeats = %v, want 12", got)
	}
	if got := tl.FromBeats(BeatsFromFloat(12)); got != 2000000 {
		t.Fatalf("fromBeats = %d, want 2000000", got)
	}
}

func TestClampTempo(t *testing.T) {
	if got := clampTempo(Timeline{Tempo: TempoFromBPM(1)}).Tempo.BPM(); got != minBPM {
		t.Fatalf("clamp low = %v", got)
	}
	if got := clampTempo(Timeline{Tempo: TempoFromBPM(5000)}).Tempo.BPM(); got != maxBPM {
		t.Fatalf("clamp high = %v", got)
	}
	if got := clampTempo(Timeline{Tempo: TempoFromBPM(120)}).Tempo.BPM(); got != 120 {
		t.Fatalf("clamp mid = %v", got)
	}
}

func TestPhaseEncodedBeatsInverse(t *testing.T) {
	tl := Timeline{Tempo: TempoFromBPM(130), BeatOrigin: BeatsFromFloat(3.7), TimeOrigin: 555555}
	q := BeatsFromFloat(4)
	for _, beat := range []float64{0, 1, 3.999, 12.5, -7.25, 1000} {
		// time corresponding to raw beat position
		raw := tl.BeatOrigin.add(BeatsFromFloat(beat))
		tm := tl.FromBeats(raw)
		encoded := ToPhaseEncodedBeats(tl, tm, q)
		decoded := FromPhaseEncodedBeats(tl, encoded, q)
		if math.Abs(float64(decoded-tm)) > 1.5*float64(q.Float()*float64(tl.Tempo.MicrosPerBeat())) {
			t.Fatalf("beat %v: decoded time %d too far from %d", beat, decoded, tm)
		}
		// The decoded time must map back to the same phase.
		if math.Abs(Phase(ToPhaseEncodedBeats(tl, decoded, q), q).Float()-Phase(encoded, q).Float()) > 1e-6 {
			t.Fatalf("beat %v: phase mismatch after roundtrip", beat)
		}
	}
}

func TestUpdateClientTimelineFromSessionContinuity(t *testing.T) {
	xform := GhostXForm{1, -1000000}
	session := Timeline{Tempo: TempoFromBPM(120), BeatOrigin: BeatsFromFloat(2), TimeOrigin: 500000}
	cur := Timeline{Tempo: TempoFromBPM(90), BeatOrigin: BeatsFromFloat(1), TimeOrigin: 0}
	at := Micros(3000000)

	nt := updateClientTimelineFromSession(cur, session, at, xform)
	if got := cur.ToBeats(at).Float(); math.Abs(nt.ToBeats(at).Float()-got) > 1e-6 {
		t.Fatalf("client timeline not continuous: %v vs %v", nt.ToBeats(at).Float(), got)
	}
	if !nt.Tempo.eq(session.Tempo) {
		t.Fatalf("session tempo not adopted: %v", nt.Tempo.BPM())
	}
}

func TestUpdateSessionTimelineFromClientPriority(t *testing.T) {
	xform := GhostXForm{1, 0}
	curSession := Timeline{Tempo: TempoFromBPM(120), BeatOrigin: BeatsFromFloat(50), TimeOrigin: 1000000}
	client := Timeline{Tempo: TempoFromBPM(140), BeatOrigin: BeatsFromFloat(4), TimeOrigin: 2000000}
	at := Micros(3000000)

	stl := updateSessionTimelineFromClient(curSession, client, at, xform)
	if !stl.Tempo.eq(client.Tempo) {
		t.Fatalf("client tempo not adopted: %v", stl.Tempo.BPM())
	}
	// The new beat origin must be strictly greater than the old one, or
	// peers would not adopt the change.
	if !stl.BeatOrigin.greater(curSession.BeatOrigin) {
		t.Fatalf("beat origin not increased: %v <= %v", stl.BeatOrigin.Float(), curSession.BeatOrigin.Float())
	}
	// No-op when nothing changed.
	if stl2 := updateSessionTimelineFromClient(stl, client, at, xform); !stl2.eq(stl) {
		t.Fatalf("expected no-op, got change: %v -> %v", stl.BeatOrigin.Float(), stl2.BeatOrigin.Float())
	}
}

func TestMedian(t *testing.T) {
	if got := median([]float64{5, 1, 3, 2, 4}); got != 3 {
		t.Fatalf("median odd = %v", got)
	}
	if got := median([]float64{4, 1, 3, 2}); got != 2.5 {
		t.Fatalf("median even = %v", got)
	}
}
