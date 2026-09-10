# abletonlink-client-go

A **pure Go** implementation of the [Ableton Link](https://ableton.github.io/link/) protocol — no cgo, no Ableton C++ library, no external dependencies beyond `golang.org/x/net`.

It discovers Link peers on the local network, synchronizes tempo and beat phase with them, syncs the transport (start/stop) state, and ships with a terminal client (`linkclient`) that displays the live tempo and phase.

Interoperates on the wire with Ableton Live, Traktor, and any other Link-enabled application.

## What's in the box

```
link/               pure Go Ableton Link implementation (library)
  link.go           public API (Link, Config, callbacks)
  engine.go         session/peer state machine (mirrors ableton::link::Controller)
  measurement.go    ping/pong clock synchronization (ghost-time offsets, median filter)
  network.go        multicast/unicast sockets, interface selection
  timeline.go       tempo/beat timeline math, phase, quantum grid helpers
  peerstate.go      wire payloads: timeline, session membership, start/stop state
  wire.go           byte-level framing (_asdp_v1 / _link_v1)
cmd/linkclient/     terminal client displaying tempo, phase and peers
```

## Usage

### The CLI client

```sh
go build -o linkclient ./cmd/linkclient
./linkclient                       # join the Link network, display tempo & phase
./linkclient -tempo 124            # start at a specific tempo
./linkclient -quantum 4            # beats per bar used for the phase display
./linkclient -startstop-sync       # sync transport start/stop with peers
./linkclient -interface en0        # pin the discovery (multicast) interface
./linkclient -unicast-interface en0 # pin the interface/IP for unicast sockets
./linkclient -v                    # verbose protocol logging to stderr
```

The client runs a [Bubble Tea](https://github.com/charmbracelet/bubbletea)
terminal UI showing the live tempo, the beat phase within the quantum (both a
continuous position bar and a per-beat metronome), the bar counter, the
transport state and all discovered peers with their tempos.

Keys:

| key | effect |
|-----|--------|
| `1`…`9` + `Enter` | propose a new tempo, e.g. `132` then Enter |
| `+` / `↑` | nudge tempo up by 1 BPM |
| `-` / `↓` | nudge tempo down by 1 BPM |
| `p` / `Space` | toggle transport playing state |
| `e` | enable / disable Link participation |
| `[` / `]` | decrease / increase display quantum |
| `q` / `Esc` / `Ctrl+C` | quit (sends bye-bye to peers) |

### The library

```go
package main

import (
	"fmt"
	"time"

	"github.com/prune998/abletonlink-client-go/link"
)

func main() {
	lnk, err := link.New(link.Config{
		Tempo:         120,
		Enabled:       true,
		StartStopSync: true,
	})
	if err != nil {
		panic(err)
	}
	defer lnk.Close()

	lnk.SetTempoCallback(func(bpm float64) { fmt.Println("tempo is now", bpm) })
	lnk.SetNumPeersCallback(func(n int) { fmt.Println("peers:", n) })

	for {
		now := time.Now()
		fmt.Printf("tempo %.2f BPM | phase %.2f/%.0f | peers %d\n",
			lnk.Tempo(), lnk.PhaseAtTime(now, 4), 4, lnk.NumPeers())
		time.Sleep(500 * time.Millisecond)
	}
}
```

API overview:

```go
lnk.Tempo() float64                     // current session tempo (BPM)
lnk.SetTempo(126, time.Now())           // propose a new tempo
lnk.PhaseAtTime(t, quantum) float64     // beat phase in [0, quantum)
lnk.BeatAtTime(t, quantum) float64      // phase-encoded beat position
lnk.TimeAtBeat(beat, quantum) time.Time // when a beat position occurs
lnk.RequestBeatAtTime(b, t, q)          // quantized beat alignment
lnk.ForceBeatAtTime(b, t, q)            // unquantized beat alignment
lnk.IsPlaying() bool                    // transport state
lnk.SetIsPlaying(true, time.Now())      // announce transport state
lnk.NumPeers() int                      // peers in the current session
lnk.Peers() []link.PeerInfo             // discovered peers (id, tempo, addr)
lnk.Enable(false)                       // leave the network (bye-bye)
```

Callbacks (`SetTempoCallback`, `SetNumPeersCallback`, `SetStartStopCallback`)
are invoked from the library's internal goroutine.

### Interfaces

A Link node uses two kinds of sockets, which can be placed on different
interfaces on multi-homed machines:

| socket | purpose | selected by |
|--------|---------|-------------|
| multicast | group membership, state broadcasts | `Config.Interface` / `-interface` |
| broadcast | multicast sends + peer responses to our broadcasts | follows the discovery interface |
| unicast | unicast responses and clock measurement | `Config.UnicastInterface` / `-unicast-interface` |

`UnicastInterface` accepts an interface name (`"en0"`), an IPv4 literal
(`"192.168.1.10"`), or `"0.0.0.0"` to bind the wildcard address (reachable
via every interface; the advertised measurement endpoint then uses the
discovery interface's address). When empty, the unicast sockets bind to the
discovery interface's address, which is the same behavior as the official
Link implementation.

## Protocol notes

This is a clean-room Go implementation of the same wire protocol used by the
official [Ableton/link](https://github.com/Ableton/link) library (Link 3+):

- **Discovery** — UDP multicast to `224.76.78.75:20808` (`_asdp_v1` framing).
  Peers broadcast their node id, session id, timeline (tempo as µs/beat, beat
  origin as µbeats, time origin as µs), transport state and measurement
  endpoint every 250 ms (TTL 5 s), and reply to each other's broadcasts with
  unicast responses. Departure is announced with a bye-bye message; silent
  peers are pruned after ~6 s.
- **Clock synchronization** — `_link_v1` ping/pong on a dedicated unicast
  socket. Exchanging >100 timed pings yields the offset between the local
  clock and the peer's "ghost" clock domain; the median makes it robust
  against jitter.
- **Sessions** — peers form sessions identified by their founding node. New
  sessions are measured against the current one and joined when their ghost
  time wins (differing by >500 ms) or, within that tolerance, by lower
  session id. Timelines propagate by strictly increasing beat origin, which
  is how tempo changes win.
- **Phase** — the session timeline's time origin marks beat 0 of the shared
  quantization grid; phase is computed locally as `beat mod quantum`.

All integers travel big endian; tempos are quantized to whole µs/beat on the
wire (identical to the reference implementation).

### Tested against the reference implementation

- The official C++ `linkhut` example (Link master): mutual peer discovery,
  session convergence, tempo propagation in both directions, and locked
  phase.
- Two or more instances of this library on the same host (integration test).
- Traktor Pro (Link-enabled) on the same network.

## Requirements

- Go 1.24+
- A network interface with IPv4 (loopback works for local testing)
- Outbound UDP to port 20808 (multicast + unicast) on the local network
- For the CLI client: a terminal (Bubble Tea; falls back gracefully when
  stdout is not a TTY)

## Troubleshooting

- **No peers found**: check that other Link applications are running, that
  UDP/multicast to `224.76.78.75:20808` is allowed by your firewall, and try
  `-interface <name>` to pin the right interface.
- **`send failed: ... sendto: no route to host` warnings**: the operating
  system is blocking UDP datagrams to the local network for the terminal the
  client runs in — this affects unicast, broadcast and multicast alike (the
  official Link applications are equally affected). The most common cause on
  modern macOS is **Local Network privacy**: grant your terminal app access
  under *System Settings → Privacy & Security → Local Network*. A VPN client
  or security tool can cause the same symptom. Loopback is not affected:
  local testing works with `-interface lo0`. Send failures are logged in a
  rate-limited fashion (detailed hint once, then a summary every 30 s).
- **VPNs / security tools** that add reject routes for `224.0.0/4` block
  Link for *all* applications (the official C++ apps included) — Link needs
  working multicast on the LAN.
- Multiple instances on one machine are supported and tested.

## License

MIT. This project is not affiliated with or endorsed by Ableton AG.
"Ableton" and "Link" are trademarks of Ableton AG.
