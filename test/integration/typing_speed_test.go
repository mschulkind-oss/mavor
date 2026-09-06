//go:build integration

package integration

import (
	"os/exec"
	"strings"
	"testing"
	"time"
)

// How fast can wtype actually type, and does -d change it?
//
// The config gained a typing_delay_ms defaulting to 1, on the reasoning that
// 1 ms is a small share of a measured ~4.4 ms per character. That reasoning is
// only sound if wtype's own default is at least 1 ms — if it is zero, the
// default made typing slower than it had been. wtype rejects `-d 0`, so the
// question cannot be answered by asking it for none.
func TestTypingSpeedAgainstDelay(t *testing.T) {
	h := sharedCompositor(t)

	text := strings.Repeat("the quick brown fox ", 20) // 400 characters
	run := func(args ...string) time.Duration {
		t.Helper()
		full := append(args, "--", text)
		cmd := exec.Command("wtype", full...)
		cmd.Env = append(cmd.Environ(),
			"XDG_RUNTIME_DIR="+h.XDGRuntime,
			"WAYLAND_DISPLAY="+h.WaylandDisp,
		)
		start := time.Now()
		if out, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("wtype %v: %v (%s)", args, err, out)
		}
		return time.Since(start)
	}

	none := run()
	d1 := run("-d", "1")
	d5 := run("-d", "5")

	perChar := func(d time.Duration) float64 {
		return float64(d.Microseconds()) / float64(len(text)) / 1000
	}
	t.Logf("%d chars: default %v (%.3f ms/char), -d 1 %v (%.3f), -d 5 %v (%.3f)",
		len(text), none, perChar(none), d1, perChar(d1), d5, perChar(d5))

	// The only assertion worth making is the ordering: a larger delay must not
	// be faster. The absolute numbers are hardware, and belong in the log.
	if d5 <= d1 {
		t.Errorf("-d 5 (%v) was not slower than -d 1 (%v); the flag may not work as assumed", d5, d1)
	}
}
