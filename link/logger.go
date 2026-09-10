package link

// Logger receives diagnostic messages from the Link engine. All methods must
// be safe for concurrent use; callbacks are invoked from the engine
// goroutine.
type Logger interface {
	Debugf(format string, args ...any)
	Infof(format string, args ...any)
	Warnf(format string, args ...any)
}

// nopLogger discards everything; it is the default.
type nopLogger struct{}

func (nopLogger) Debugf(string, ...any) {}
func (nopLogger) Infof(string, ...any)  {}
func (nopLogger) Warnf(string, ...any)  {}

// PrintfLogger adapts a printf-style function (e.g. log.Printf) to the
// Logger interface.
type PrintfLogger struct {
	Printf func(format string, args ...any)
	Debug  bool
}

func (l PrintfLogger) Debugf(format string, args ...any) {
	if l.Debug && l.Printf != nil {
		l.Printf("link: debug: "+format+"\n", args...)
	}
}

func (l PrintfLogger) Infof(format string, args ...any) {
	if l.Printf != nil {
		l.Printf("link: info: "+format+"\n", args...)
	}
}

func (l PrintfLogger) Warnf(format string, args ...any) {
	if l.Printf != nil {
		l.Printf("link: warn: "+format+"\n", args...)
	}
}
