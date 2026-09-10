package main

import (
	"os"
	"runtime"
	"strings"
	"testing"
	"time"

	tea "github.com/charmbracelet/bubbletea"

	"github.com/prune998/abletonlink-client-go/link"
)

// testLoopback returns the loopback interface name for the current platform,
// overridable via ABLINK_TEST_INTERFACE. Windows runners have no usable
// loopback multicast, so tests using a live Link node skip there.
func testLoopback(t *testing.T) string {
	t.Helper()
	if env := os.Getenv("ABLINK_TEST_INTERFACE"); env != "" {
		return env
	}
	switch runtime.GOOS {
	case "windows":
		t.Skip("loopback multicast not available on windows")
	case "linux":
		return "lo"
	default:
		return "lo0"
	}
	return ""
}

// skipIfNetworkUnavailable lets tests skip gracefully in environments (CI
// runners, containers) where loopback multicast is not usable, while genuine
// failures still fail loudly.
func skipIfNetworkUnavailable(t *testing.T, err error) {
	t.Helper()
	if err == nil {
		return
	}
	if strings.Contains(err.Error(), "interface") ||
		strings.Contains(err.Error(), "multicast") ||
		strings.Contains(err.Error(), "socket") {
		t.Skipf("network setup unavailable in this environment: %v", err)
	}
	t.Fatalf("network setup failed: %v", err)
}

func newTestModel(t *testing.T) model {
	t.Helper()
	lnk, err := link.New(link.Config{Tempo: 120, Enabled: true, Interface: testLoopback(t)})
	skipIfNetworkUnavailable(t, err)
	t.Cleanup(func() { lnk.Close() })
	return *newModel(lnk, 4)
}

func key(r rune) tea.KeyMsg {
	return tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{r}}
}

func TestQuitKey(t *testing.T) {
	m := newTestModel(t)
	_, cmd := m.Update(key('q'))
	if cmd == nil {
		t.Fatal("quit key did not produce a command")
	}
	// bubbletea's tea.Quit returns a quitMsg sentinel which the runtime
	// interprets as "exit the program".
	done := make(chan tea.Msg, 1)
	go func() { done <- cmd() }()
	select {
	case msg := <-done:
		if msg == nil {
			t.Fatal("quit command returned nil msg")
		}
	case <-time.After(2 * time.Second):
		t.Fatal("quit command blocked")
	}
}

func TestTempoEntryViaKeys(t *testing.T) {
	lnk, err := link.New(link.Config{Tempo: 120, Enabled: true, Interface: testLoopback(t)})
	skipIfNetworkUnavailable(t, err)
	t.Cleanup(func() { lnk.Close() })

	// Other Link applications may be running on the network and may outvote
	// our tempo change; deterministically verify the change was applied by
	// observing the tempo callback. Registered after newModel, which wires
	// up its own callbacks.
	m := *newModel(lnk, 4)
	tempoSeen := make(chan float64, 8)
	lnk.SetTempoCallback(func(bpm float64) {
		select {
		case tempoSeen <- bpm:
		default:
		}
	})

	// Type "132" then Enter -> tempo proposal of 132 BPM.
	for _, k := range []rune{'1', '3', '2'} {
		updated, _ := m.Update(key(k))
		m = updated.(model)
	}
	if m.input != "132" {
		t.Fatalf("input buffer = %q, want %q", m.input, "132")
	}

	updated, _ := m.Update(tea.KeyMsg{Type: tea.KeyEnter})
	m = updated.(model)
	if m.input != "" {
		t.Fatalf("input buffer not cleared: %q", m.input)
	}

	deadline := time.After(5 * time.Second)
	for {
		select {
		case bpm := <-tempoSeen:
			if diff := bpm - 132; diff < 0.5 && diff > -0.5 {
				return
			}
		case <-deadline:
			t.Fatalf("tempo callback never reported 132 (current tempo %v)", lnk.Tempo())
		}
	}
}

func TestNudgeAndPlayingKeys(t *testing.T) {
	m := newTestModel(t)
	start := m.lnk.Tempo()

	updated, _ := m.Update(key('+'))
	m = updated.(model)
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		if m.lnk.Tempo() >= start+0.5 {
			break
		}
		time.Sleep(50 * time.Millisecond)
	}
	if got := m.lnk.Tempo(); got < start+0.5 {
		t.Fatalf("tempo nudge did not propagate: start=%v got=%v", start, got)
	}

	updated, _ = m.Update(key('p'))
	m = updated.(model)
	deadline = time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		if m.lnk.IsPlaying() {
			return
		}
		time.Sleep(50 * time.Millisecond)
	}
	t.Fatal("transport did not start playing after 'p'")
}

func TestQuantumKeys(t *testing.T) {
	m := newTestModel(t) // quantum 4
	updated, _ := m.Update(key('['))
	m = updated.(model)
	if m.quantum != 3 {
		t.Fatalf("quantum after '[' = %v, want 3", m.quantum)
	}
	updated, _ = m.Update(key(']'))
	updated, _ = updated.(model).Update(key(']'))
	m = updated.(model)
	if m.quantum != 5 {
		t.Fatalf("quantum after ']' twice = %v, want 5", m.quantum)
	}
	// Floor at 1.
	m.quantum = 1
	updated, _ = m.Update(key('['))
	m = updated.(model)
	if m.quantum != 1 {
		t.Fatalf("quantum floor violated: %v", m.quantum)
	}
}

func TestViewRenders(t *testing.T) {
	m := newTestModel(t)
	v := m.View()
	for _, want := range []string{"tempo", "phase", "peers", "pure Go Ableton Link client"} {
		if !strings.Contains(v, want) {
			t.Errorf("view missing %q", want)
		}
	}
	if len(v) == 0 {
		t.Fatal("empty view")
	}
}
