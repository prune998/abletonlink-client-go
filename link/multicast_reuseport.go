//go:build darwin || linux || freebsd || netbsd || openbsd || dragonfly

package link

import (
	"syscall"

	"golang.org/x/sys/unix"
)

// multicastListenControl returns the ListenConfig.Control hook that sets the
// socket options needed to share UDP port 20808 with other Link applications
// on unix systems: SO_REUSEADDR plus SO_REUSEPORT.
//
// SO_REUSEPORT is required on macOS so that our socket lands in the same
// reuse group as other Go-based Link participants; multicast datagrams are
// delivered per reuse group there. Windows has no SO_REUSEPORT; its default
// listener options already permit sharing the port (see
// multicast_windows.go), and other unix platforms get the same fallback.
func multicastListenControl() func(network, address string, c syscall.RawConn) error {
	return func(network, address string, c syscall.RawConn) error {
		var sockErr error
		if err := c.Control(func(fd uintptr) {
			sockErr = unix.SetsockoptInt(int(fd), unix.SOL_SOCKET, unix.SO_REUSEADDR, 1)
			if sockErr != nil {
				return
			}
			sockErr = unix.SetsockoptInt(int(fd), unix.SOL_SOCKET, unix.SO_REUSEPORT, 1)
		}); err != nil {
			return err
		}
		return sockErr
	}
}
