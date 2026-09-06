//go:build integration

package integration

import (
	"testing"
	"time"

	"github.com/mschulkind-oss/mavor/internal/ipc"
)

// The end-to-end shape of hide_show_test.go: the real daemon binary, a real
// compositor, and a state cycle.
//
// The overlay tests elsewhere in this package drive overlay.NewDefault
// directly. That has repeatedly passed while the thing a user runs was broken,
// because the daemon builds the overlay alongside everything else it opens —
// a second Wayland connection for typing, a recorder, a preview — and the
// failures have all been in that combination rather than in the overlay alone.
//
// This drives the real daemon binary and watches the overlay through a state
// cycle. It needs no audio, so unlike the transcription tests it runs
// everywhere the compositor does.
func TestDaemonOverlaySurvivesAStateCycle(t *testing.T) {
	h := Start(t, Options{Width: testWidth, Height: testHeight})
	socket, _ := h.RunDaemon(t.Context(), MavorBinary, "whisper-tiny.en")

	overlayVisible := func(when string) bool {
		t.Helper()
		bands := findBrightBands(decodePNG(t, h.Grim()))
		// Waybar is not running in this harness configuration, so any band
		// below the very top is the overlay.
		t.Logf("%s: bands=%v", when, bands)
		return len(bands) > 0
	}

	toggle := func(want string) {
		t.Helper()
		r, err := ipc.Send(socket, ipc.Request{Action: "toggle"}, 3*time.Second)
		if err != nil {
			t.Fatalf("toggle: %v", err)
		}
		if r.State != want {
			t.Fatalf("toggle returned %q, want %q", r.State, want)
		}
	}

	// Recording: the pill must reach the screen.
	toggle("recording")
	time.Sleep(400 * time.Millisecond)
	if !overlayVisible("recording") {
		t.Error("nothing on screen while recording — the overlay never appeared")
	}

	// Back to idle, then round again. A render loop that died during the
	// first cycle leaves the last frame on screen, so only a SECOND cycle
	// distinguishes a live overlay from a corpse.
	toggle("transcribing")
	time.Sleep(600 * time.Millisecond)

	deadline := time.Now().Add(5 * time.Second)
	for {
		r, err := ipc.Send(socket, ipc.Request{Action: "status"}, 2*time.Second)
		if err != nil {
			t.Fatalf("status: %v", err)
		}
		if r.State == "idle" {
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("stuck in %q", r.State)
		}
		time.Sleep(100 * time.Millisecond)
	}

	toggle("recording")
	time.Sleep(400 * time.Millisecond)
	if !overlayVisible("second recording") {
		t.Fatal("the overlay is gone on the second cycle — its render loop died during the first")
	}
}
