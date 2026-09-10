package link

// Beats represents a position on the Link timeline with micro-beat
// precision, mirroring ableton::link::Beats (an int64 count of
// 1/1,000,000 beat).
type Beats struct{ micro int64 }

// BeatsFromFloat creates Beats from a floating point beat value.
func BeatsFromFloat(b float64) Beats { return Beats{llround(b * 1e6)} }

// BeatsFromMicro creates Beats directly from micro-beats.
func BeatsFromMicro(micro int64) Beats { return Beats{micro} }

// Float returns the beat position as a float64.
func (b Beats) Float() float64 { return float64(b.micro) / 1e6 }

// MicroBeats returns the internal micro-beat count.
func (b Beats) MicroBeats() int64 { return b.micro }

func (b Beats) neg() Beats           { return Beats{-b.micro} }
func (b Beats) add(o Beats) Beats    { return Beats{b.micro + o.micro} }
func (b Beats) sub(o Beats) Beats    { return Beats{b.micro - o.micro} }
func (b Beats) mod(o Beats) Beats    { return Beats{b.micro % o.micro} }
func (b Beats) less(o Beats) bool    { return b.micro < o.micro }
func (b Beats) greater(o Beats) bool { return b.micro > o.micro }
func (b Beats) eq(o Beats) bool      { return b.micro == o.micro }

func absBeats(b Beats) Beats {
	if b.micro < 0 {
		return Beats{-b.micro}
	}
	return b
}

// Phase returns a value in [0, quantum) corresponding to beats % quantum,
// with negative beat values handled correctly. A quantum of zero returns
// zero. Mirrors ableton::link::phase.
func Phase(beats, quantum Beats) Beats {
	if quantum.micro == 0 {
		return Beats{0}
	}
	qm := quantum.micro
	quantumBins := (abs64(beats.micro) + qm) / qm
	quantumBeats := quantumBins * qm
	return (beats.add(Beats{quantumBeats})).mod(quantum)
}

// NextPhaseMatch returns the least value greater than x that matches the
// phase of target with respect to the given quantum.
func NextPhaseMatch(x, target, quantum Beats) Beats {
	desiredPhase := Phase(target, quantum)
	xPhase := Phase(x, quantum)
	phaseDiff := desiredPhase.sub(xPhase).add(quantum).mod(quantum)
	return x.add(phaseDiff)
}

// ClosestPhaseMatch returns the closest value to x that matches the phase of
// target with respect to the given quantum. The result deviates from x by at
// most quantum/2, but may be less than x.
func ClosestPhaseMatch(x, target, quantum Beats) Beats {
	return NextPhaseMatch(x.sub(Beats{llround(0.5 * quantum.Float())}), target, quantum)
}

func abs64(v int64) int64 {
	if v < 0 {
		return -v
	}
	return v
}
