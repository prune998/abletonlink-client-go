//go:build unix && !darwin && !linux && !freebsd && !netbsd && !openbsd && !dragonfly

package link

import "syscall"

// multicastListenControl returns the ListenConfig.Control hook for unix
// platforms without a portable SO_REUSEPORT constant. The Go runtime already
// sets SO_REUSEADDR on listening sockets, which is sufficient for sharing
// the port in most cases.
func multicastListenControl() func(network, address string, c syscall.RawConn) error {
	return nil
}
