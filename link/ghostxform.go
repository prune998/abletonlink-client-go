package link

// GhostXForm maps host time to and from the shared "ghost" clock domain,
// mirroring ableton::link::GhostXForm. Ghost time = slope * hostTime +
// intercept. When peers join a session they measure this transform against a
// session member so that all peers agree on a common time domain.
type GhostXForm struct {
	Slope     float64
	Intercept Micros
}

// ZeroXForm is the zero value; it is used to signal a failed measurement.
func ZeroXForm() GhostXForm { return GhostXForm{} }

func (x GhostXForm) eq(o GhostXForm) bool {
	return x.Slope == o.Slope && x.Intercept == o.Intercept
}

// HostToGhost maps a host time to ghost time.
func (x GhostXForm) HostToGhost(hostTime Micros) Micros {
	return Micros(llround(x.Slope*float64(hostTime))) + x.Intercept
}

// GhostToHost maps a ghost time back to host time.
func (x GhostXForm) GhostToHost(ghostTime Micros) Micros {
	return Micros(llround(float64(ghostTime-x.Intercept) / x.Slope))
}

// initXForm makes the current time map to a ghost time of 0 with ghost time
// increasing at the same rate as clock time.
func initXForm(now Micros) GhostXForm {
	return GhostXForm{1.0, -now}
}
