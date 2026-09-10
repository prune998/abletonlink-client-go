// Command linkclient is a pure Go Ableton Link client with a terminal UI
// built on Bubble Tea. It joins the Link network, displays the current
// tempo, beat phase and peers in real time, and lets you propose tempo,
// phase and transport changes.
//
// Keys:
//
//	digits+Enter   set tempo, e.g. 132 then Enter
//	+ / -          nudge tempo up / down by 1 BPM
//	p or space     toggle transport playing state
//	e              enable / disable Link participation
//	[ / ]          decrease / increase display quantum
//	q / esc / ctrl+c   quit
package main

import (
	"flag"
	"fmt"
	"log"
	"os"
	"os/signal"
	"strconv"
	"strings"
	"syscall"
	"time"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"

	"github.com/prune998/abletonlink-client-go/link"
)

// version is overridden at build time via -ldflags "-X main.version=...".
var version = "dev"

func main() {
	tempo := flag.Float64("tempo", 120, "initial tempo in BPM")
	quantum := flag.Float64("quantum", 4, "quantum (beats per bar) used for the phase display")
	iface := flag.String("interface", "", "network interface to use for discovery (default: auto)")
	ucIface := flag.String("unicast-interface", "", "interface or local IP for unicast sockets (default: discovery interface)")
	startStop := flag.Bool("startstop-sync", false, "enable transport start/stop sync")
	verbose := flag.Bool("v", false, "verbose protocol logging to stderr")
	showVersion := flag.Bool("version", false, "print version and exit")
	flag.Parse()

	if *showVersion {
		fmt.Println("linkclient " + version)
		return
	}

	logger := link.PrintfLogger{Printf: func(f string, a ...any) {
		// The TUI owns the terminal via the alternate screen; diagnostics
		// go to stderr where they appear above the UI (intended for -v
		// debugging sessions).
		fmt.Fprintf(os.Stderr, f, a...)
	}, Debug: *verbose}

	lnk, err := link.New(link.Config{
		Tempo:            *tempo,
		Enabled:          true,
		StartStopSync:    *startStop,
		Interface:        *iface,
		UnicastInterface: *ucIface,
		Logger:           logger,
	})
	if err != nil {
		log.Fatalf("failed to create Link node: %v", err)
	}

	if !interactive() {
		// No terminal available (e.g. output piped to a file): fall back to
		// plain periodic status lines.
		runPlain(lnk, *quantum)
		return
	}

	m := newModel(lnk, *quantum)
	p := tea.NewProgram(m, tea.WithAltScreen())

	if _, err := p.Run(); err != nil {
		lnk.Close()
		log.Fatalf("ui error: %v", err)
	}

	// Leaving the TUI announces our departure to Link peers (bye-bye).
	if err := lnk.Close(); err != nil {
		log.Fatalf("error while closing link: %v", err)
	}
}

// interactive reports whether a terminal is available for the TUI. Bubble
// Tea reads key input from /dev/tty when stdin is not a terminal, so that is
// the resource whose availability decides between TUI and plain mode.
func interactive() bool {
	tty, err := os.Open("/dev/tty")
	if err != nil {
		return false
	}
	_ = tty.Close()
	return true
}

// runPlain prints one status line per interval until interrupted.
func runPlain(lnk *link.Link, quantum float64) {
	sig := make(chan os.Signal, 1)
	signal.Notify(sig, syscall.SIGINT, syscall.SIGTERM)
	done := make(chan struct{})
	go func() {
		<-sig
		close(done)
	}()

	fmt.Println("pure Go Ableton Link client (non-interactive mode; Ctrl-C to quit)")
	ticker := time.NewTicker(500 * time.Millisecond)
	defer ticker.Stop()
	for {
		select {
		case <-done:
			_ = lnk.Close()
			return
		case <-ticker.C:
			now := time.Now()
			playing := "stopped"
			if lnk.IsPlaying() {
				playing = "playing"
			}
			fmt.Printf("tempo %7.2f BPM | phase %4.2f/%.0f | peers %d | %s\n",
				lnk.Tempo(), lnk.PhaseAtTime(now, quantum), quantum,
				lnk.NumPeers(), playing)
		}
	}
}

// ---------------------------------------------------------------------------
// Bubble Tea model
// ---------------------------------------------------------------------------

type tickMsg time.Time

// updateMsg nudges the model to re-read the Link state; it is fed from the
// Link engine's callbacks through a channel.
type updateMsg struct{}

type model struct {
	lnk     *link.Link
	quantum float64

	// Link state snapshot, refreshed from the link package on every tick.
	tempo         float64
	peers         []link.PeerInfo
	numPeers      int
	playing       bool
	enabled       bool
	startStopSync bool
	nodeID        link.NodeId
	sessionID     link.NodeId

	input  string // tempo input buffer
	width  int
	height int

	updates chan struct{}
}

func newModel(lnk *link.Link, quantum float64) *model {
	if quantum < 1 {
		quantum = 4
	}
	m := &model{
		lnk:     lnk,
		quantum: quantum,
		updates: make(chan struct{}, 1),
	}
	m.refresh()

	// Forward Link engine events into the Bubble Tea event loop.
	notify := func() {
		select {
		case m.updates <- struct{}{}:
		default:
		}
	}
	lnk.SetNumPeersCallback(func(int) { notify() })
	lnk.SetTempoCallback(func(float64) { notify() })
	lnk.SetStartStopCallback(func(bool) { notify() })

	return m
}

// refresh pulls a fresh snapshot of the Link state.
func (m *model) refresh() {
	m.tempo = m.lnk.Tempo()
	m.peers = m.lnk.Peers()
	m.numPeers = m.lnk.NumPeers()
	m.playing = m.lnk.IsPlaying()
	m.enabled = m.lnk.Enabled()
	m.startStopSync = m.lnk.StartStopSyncEnabled()
	m.nodeID = m.lnk.NodeID()
	m.sessionID = m.lnk.SessionID()
}

func (m model) Init() tea.Cmd {
	return tea.Batch(tick(), waitForUpdate(m.updates))
}

func tick() tea.Cmd {
	return tea.Tick(100*time.Millisecond, func(t time.Time) tea.Msg {
		return tickMsg(t)
	})
}

func waitForUpdate(ch chan struct{}) tea.Cmd {
	return func() tea.Msg {
		<-ch
		return updateMsg{}
	}
}

func (m model) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	switch msg := msg.(type) {
	case tea.WindowSizeMsg:
		m.width, m.height = msg.Width, msg.Height
		return m, nil

	case tickMsg:
		m.refresh()
		return m, tick()

	case updateMsg:
		m.refresh()
		return m, waitForUpdate(m.updates)

	case tea.KeyMsg:
		switch msg.String() {
		case "q", "esc", "ctrl+c":
			return m, tea.Quit
		case "up", "+", "=":
			m.lnk.SetTempo(m.tempo+1, time.Now())
		case "down", "-":
			m.lnk.SetTempo(m.tempo-1, time.Now())
		case "p", " ":
			m.lnk.SetIsPlaying(!m.playing, time.Now())
		case "e":
			m.lnk.Enable(!m.enabled)
		case "[":
			if m.quantum > 1 {
				m.quantum--
			}
		case "]":
			if m.quantum < 32 {
				m.quantum++
			}
		case "enter":
			if m.input != "" {
				if bpm, err := strconv.ParseFloat(m.input, 64); err == nil && bpm >= 20 && bpm <= 999 {
					m.lnk.SetTempo(bpm, time.Now())
				}
				m.input = ""
			}
		case "backspace":
			if m.input != "" {
				m.input = m.input[:len(m.input)-1]
			}
		default:
			// Accumulate digits and the decimal point for tempo entry.
			s := msg.String()
			if len(s) == 1 && (s[0] >= '0' && s[0] <= '9' || s == ".") && len(m.input) < 6 {
				m.input += s
			}
		}
		return m, nil
	}
	return m, nil
}

// ---------------------------------------------------------------------------
// View
// ---------------------------------------------------------------------------

var (
	titleStyle = lipgloss.NewStyle().
			Bold(true).
			Foreground(lipgloss.Color("63")).
			Padding(0, 1)

	labelStyle = lipgloss.NewStyle().
			Foreground(lipgloss.Color("243"))

	valueStyle = lipgloss.NewStyle().
			Bold(true).
			Foreground(lipgloss.Color("252"))

	phaseStyle = lipgloss.NewStyle().
			Foreground(lipgloss.Color("39"))

	playingStyle = lipgloss.NewStyle().
			Bold(true).
			Foreground(lipgloss.Color("42"))

	stoppedStyle = lipgloss.NewStyle().
			Foreground(lipgloss.Color("214"))

	disabledStyle = lipgloss.NewStyle().
			Bold(true).
			Foreground(lipgloss.Color("196"))

	peerStyle = lipgloss.NewStyle().
			Foreground(lipgloss.Color("245"))

	metroStyle = lipgloss.NewStyle().
			Foreground(lipgloss.Color("75"))

	boxStyle = lipgloss.NewStyle().
			Border(lipgloss.RoundedBorder()).
			BorderForeground(lipgloss.Color("60")).
			Padding(0, 2, 1, 2)

	helpStyle = lipgloss.NewStyle().
			Foreground(lipgloss.Color("241"))

	inputStyle = lipgloss.NewStyle().
			Bold(true).
			Foreground(lipgloss.Color("220"))
)

const barWidth = 32

func (m model) View() string {
	c := func(s lipgloss.Style, txt string) string { return s.Render(txt) }

	transport := c(stoppedStyle, "■ stopped")
	if m.playing {
		transport = c(playingStyle, "▶ playing")
	}
	state := c(valueStyle, "enabled")
	if !m.enabled {
		state = c(disabledStyle, "disabled")
	}

	beat := m.lnk.BeatAtTime(time.Now(), m.quantum)
	phase := m.lnk.PhaseAtTime(time.Now(), m.quantum)
	bar := int64(beat / m.quantum)

	// Fine-grained phase position within the quantum.
	pos := int(phase / m.quantum * barWidth)
	if pos >= barWidth {
		pos = barWidth - 1
	}
	var fine strings.Builder
	for i := 0; i < barWidth; i++ {
		switch {
		case i < pos:
			fine.WriteString("·")
		case i == pos:
			fine.WriteString("●")
		default:
			fine.WriteString(" ")
		}
	}

	// One marker per beat of the quantum (metronome view).
	var metro strings.Builder
	for i := 0; i < int(m.quantum); i++ {
		if int(phase) == i {
			metro.WriteString("◉ ")
		} else {
			metro.WriteString("○ ")
		}
	}

	rows := []string{
		c(titleStyle, "pure Go Ableton Link client"),
		"",
		fmt.Sprintf("%s %s   %s %s   %s %s",
			c(labelStyle, "tempo"),
			c(valueStyle, fmt.Sprintf("%7.2f BPM", m.tempo)),
			c(labelStyle, "transport"), transport,
			c(labelStyle, "link"), state),
		fmt.Sprintf("%s %s   %s %s   %s %s",
			c(labelStyle, "phase"),
			c(phaseStyle, fmt.Sprintf("%4.2f / %.0f", phase, m.quantum)),
			c(labelStyle, "bar"), c(phaseStyle, fmt.Sprintf("%d", bar)),
			c(labelStyle, "quantum"), c(valueStyle, fmt.Sprintf("%.0f", m.quantum))),
		"  " + c(metroStyle, metro.String()),
		"  " + c(phaseStyle, fine.String()),
		"",
		peersView(m),
	}

	if m.input != "" {
		rows = append(rows, fmt.Sprintf("%s %s",
			c(labelStyle, "tempo?"),
			c(inputStyle, m.input+"▏")))
	}

	body := lipgloss.JoinVertical(lipgloss.Left, rows...)
	return boxStyle.Render(body) + "\n" + helpView()
}

func peersView(m model) string {
	if len(m.peers) == 0 {
		return labelStyle.Render("waiting for peers on the Link network…")
	}
	var b strings.Builder
	b.WriteString(labelStyle.Render(fmt.Sprintf("peers (%d in session)", m.numPeers)))
	for _, p := range m.peers {
		addr := "-"
		if p.Addr != nil {
			addr = p.Addr.String()
		}
		b.WriteString("\n" + peerStyle.Render(fmt.Sprintf("  %s  %-21s %8.2f BPM",
			p.NodeID.String(), addr, p.Tempo)))
	}
	return b.String()
}

func helpView() string {
	return helpStyle.Render(
		"↑/+ tempo up · ↓/- tempo down · digits+enter: set tempo · p play/stop · e link on/off · [ ] quantum · q quit")
}
