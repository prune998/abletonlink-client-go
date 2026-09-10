//go:build windows

package link

import "syscall"

// multicastListenControl returns the ListenConfig.Control hook for Windows.
// The Go runtime already sets SO_REUSEADDR on listening sockets there, which
// is sufficient to share UDP port 20808 with other Link applications; there
// is no SO_REUSEPORT on Windows, so no extra options are required.
func multicastListenControl() func(network, address string, c syscall.RawConn) error {
	return nil
}
