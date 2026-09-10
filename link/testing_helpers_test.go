package link

import (
	"os"
	"runtime"
	"strings"
	"testing"
)

// loopbackName returns the loopback interface name for the current platform,
// overridable via ABLINK_TEST_INTERFACE. On Windows runners there is no
// usable loopback multicast, so those tests skip.
func loopbackName(t *testing.T) string {
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

// skipIfUnavailable lets integration tests skip gracefully in environments
// (CI runners, containers) where loopback multicast is not usable, while
// genuine failures still fail loudly.
func skipIfUnavailable(t *testing.T, err error) {
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
