package link

// Tempo is a tempo in beats per minute, mirroring ableton::link::Tempo. On
// the wire it is encoded as whole microseconds per beat.
type Tempo struct{ bpm float64 }

// TempoFromBPM creates a Tempo from beats per minute.
func TempoFromBPM(bpm float64) Tempo { return Tempo{bpm} }

// BPM returns the tempo in beats per minute.
func (t Tempo) BPM() float64 { return t.bpm }

// MicrosPerBeat returns the tempo as whole microseconds per beat.
func (t Tempo) MicrosPerBeat() int64 { return llround(60 * 1e6 / t.bpm) }

// MicrosToBeats converts a duration in microseconds to a beat position.
func (t Tempo) MicrosToBeats(m Micros) Beats {
	return BeatsFromFloat(float64(m) / float64(t.MicrosPerBeat()))
}

// BeatsToMicros converts a beat position to a duration in microseconds.
func (t Tempo) BeatsToMicros(b Beats) Micros {
	return Micros(llround(b.Float() * float64(t.MicrosPerBeat())))
}

func (t Tempo) eq(o Tempo) bool { return t.bpm == o.bpm }
