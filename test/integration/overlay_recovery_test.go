//go:build integration

package integration

import (
	"testing"
	"time"
)

// OverlayVisibleOn reports whether anything is drawn on the named output. The
// overlay is the only thing this harness ever puts on a black backdrop.
//
// Lives here rather than in harness.go because the image helpers behind it are
// integration-tagged and the harness also builds under e2e.
func (h *Harness) OverlayVisibleOn(output string) bool {
	h.t.Helper()
	return len(findBrightBands(decodePNG(h.t, h.grim(output)))) > 0
}

// WaitForOverlayOn polls until the overlay is drawn on the named output.
// Recovery is deliberately not instant — a failed rebuild backs off for two
// seconds before trying again — so a test must wait for it rather than sample
// once and conclude it never came back.
func (h *Harness) WaitForOverlayOn(output string, within time.Duration) bool {
	h.t.Helper()
	deadline := time.Now().Add(within)
	for {
		if h.OverlayVisibleOn(output) {
			return true
		}
		if time.Now().After(deadline) {
			return false
		}
		time.Sleep(250 * time.Millisecond)
	}
}

// TestOverlayRecoversAfterAGapWithNoOutputAtAll covers the rebuild that fails
// before it succeeds, which is the case the first rebuild could not survive.
//
// TestOverlayReturnsAfterOutputUnplug swaps one monitor for another, so the
// rebuild finds somewhere to go on its first attempt. Real hardware rarely
// obliges: a dock is unplugged and replugged seconds later, a DisplayPort link
// drops and re-trains, a KVM switches away and back. In all of them there is a
// window with no output at all, and a layer surface asked for during it is
// closed by the compositor as soon as it is created.
//
// rebuild used to destroy the surface and close the connection BEFORE dialling
// a replacement, so a failure left the render loop holding a closed display.
// The loop dispatches on that display every iteration, so the next tick — half
// a second later — turned "retry in 2s" into "use of closed network
// connection" and stopped the loop for good, 1.5 seconds before the retry it
// had just scheduled. The overlay was gone until the daemon restarted, while
// recording, ducking and typing carried on, which is what made it read as the
// overlay alone having broken.
//
// The gap here is deliberately longer than several retry periods: the point is
// not that one rebuild failed but that the loop was still alive to keep trying
// when an output finally came back.
func TestOverlayRecoversAfterAGapWithNoOutputAtAll(t *testing.T) {
	h := Start(t, Options{Width: testWidth, Height: testHeight})
	socket, _ := h.RunDaemon(t.Context(), MavorBinary, "whisper-tiny.en")

	// Baseline. Without this the rest of the test cannot tell "recovered"
	// from "was never drawn in the first place".
	h.ShowOverlay(socket)
	if !h.WaitForOverlayOn("HEADLESS-1", 2*time.Second) {
		t.Fatal("overlay not visible before the outage — the test proves nothing")
	}

	// Recording continues across the outage. The user did not stop talking
	// because their monitor blinked, and a rebuild is supposed to carry the
	// scene over, so the overlay must come back WITHOUT anyone asking for it
	// again.
	h.swaymsg("output", "HEADLESS-1", "unplug")

	// Long enough for several rebuilds to be attempted and fail. Before the
	// fix the loop was dead within one tick of the first one.
	time.Sleep(5 * time.Second)

	// The daemon must have survived the outage. A dead daemon and a dead
	// render loop look identical in a screenshot, and only one of them is
	// this test's subject.
	if state, err := h.DaemonState(socket); err != nil {
		t.Fatalf("daemon stopped answering during the outage: %v", err)
	} else if state != "recording" {
		t.Fatalf("daemon left recording during the outage: state = %q", state)
	}

	// Give it somewhere to go again.
	h.swaymsg("create_output")
	h.swaymsg("output", "HEADLESS-2", "mode", "1920x1080")
	h.swaymsg("output", "HEADLESS-2", "bg", "#000000", "solid_color")

	if !h.WaitForOverlayOn("HEADLESS-2", 10*time.Second) {
		t.Fatal("overlay never came back after a gap with no output: the rebuild " +
			"that failed during the gap took the render loop with it, so the retry " +
			"that would have found HEADLESS-2 never ran")
	}
}

// TestOverlayRecoversAfterCompositorRestart covers losing the connection
// rather than the surface.
//
// A compositor restart takes the socket, not just the layer surface, so it
// arrives as a dispatch error rather than as zwlr_layer_surface_v1.closed. The
// surface-closed recovery never saw it: the error went straight to the render
// loop's terminal failure path, and the overlay was gone for the life of the
// daemon even with a compositor running again a second later. Recording,
// ducking and typing carry on regardless, so nothing else reports it — the
// same silent failure the rebuild was written for, reached by the other door.
//
// The daemon keeps the WAYLAND_DISPLAY it was spawned with, so recovery is
// only possible if the replacement compositor takes the same socket name.
// RestartCompositor asserts that rather than assuming it.
func TestOverlayRecoversAfterCompositorRestart(t *testing.T) {
	h := Start(t, Options{Width: testWidth, Height: testHeight})
	socket, _ := h.RunDaemon(t.Context(), MavorBinary, "whisper-tiny.en")

	h.ShowOverlay(socket)
	if !h.WaitForOverlayOn("HEADLESS-1", 2*time.Second) {
		t.Fatal("overlay not visible before the restart — the test proves nothing")
	}
	h.HideOverlay(socket)
	h.WaitForState(socket, "idle")

	h.RestartCompositor(Options{Width: testWidth, Height: testHeight})

	// The daemon outlives its compositor. This is the assertion that would
	// have failed had the overlay been built on a toolkit that exits when its
	// display goes away.
	if _, err := h.DaemonState(socket); err != nil {
		t.Fatalf("daemon did not survive the compositor restart: %v", err)
	}

	h.ShowOverlay(socket)
	if !h.WaitForOverlayOn("HEADLESS-1", 10*time.Second) {
		t.Fatal("overlay never came back after the compositor restarted: the render " +
			"loop treated the lost connection as fatal instead of rebuilding")
	}
}
