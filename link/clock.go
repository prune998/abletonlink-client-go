package link

import (
	"math"
	"time"
)

// Micros is a count of microseconds on the local monotonic clock. Link
// timelines map this value to a beat position.
type Micros int64

var clockStart = time.Now()

// Now returns the current time in microseconds on the local monotonic clock.
// The epoch is the process start time, mirroring the behavior of the official
// Link clock implementations (a monotonic clock with an arbitrary but stable
// epoch).
func Now() Micros {
	return Micros(int64(time.Since(clockStart) / time.Microsecond))
}

// Time converts a microsecond value back to a wall-clock time.Time.
func (m Micros) Time() time.Time {
	return clockStart.Add(time.Duration(m) * time.Microsecond)
}

// MicrosFromTime converts a wall-clock time to monotonic microseconds. If the
// time carries a monotonic reading it is used, otherwise the result is
// computed from the wall clock, which may be discontinuous.
func MicrosFromTime(t time.Time) Micros {
	if t.Before(clockStart) {
		return 0
	}
	return Micros(int64(t.Sub(clockStart) / time.Microsecond))
}

func llround(v float64) int64 {
	return int64(math.Round(v))
}
