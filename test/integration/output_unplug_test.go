//go:build integration

package integration

import (
	"testing"
	"time"
)

// TestOverlayReturnsAfterOutputUnplug reproduces a monitor being unplugged
// underneath a running daemon.
//
// The overlay's layer surface is created once, at startup, with a null output
// — the compositor picks one. When that output goes away the compositor sends
// zwlr_layer_surface_v1.closed and destroys the surface. The render loop went
// on painting into it for the life of the daemon, so the overlay never came
// back: every frame was drawn, committed, and dropped on the floor. Recording
// and ducking kept working, which is what made it look like the overlay alone
// had broken.
//
// A DP link that drops and re-trains does exactly this without anyone
// touching a cable.
func TestOverlayReturnsAfterOutputUnplug(t *testing.T) {
	h := Start(t, Options{Width: testWidth, Height: testHeight})
	socket, _ := h.RunDaemon(t.Context(), MavorBinary, "whisper-tiny.en")

	// Baseline: the overlay shows on the only output there is.
	h.ShowOverlay(socket)
	if !h.WaitForOverlayOn("HEADLESS-1", 2*time.Second) {
		t.Fatal("overlay not visible on HEADLESS-1 before the unplug — the test proves nothing")
	}
	h.HideOverlay(socket)

	// Swap the monitor out from under it.
	h.swaymsg("create_output")
	h.swaymsg("output", "HEADLESS-2", "mode", "1920x1080")
	h.swaymsg("output", "HEADLESS-2", "bg", "#000000", "solid_color")
	h.swaymsg("output", "HEADLESS-1", "unplug")
	time.Sleep(500 * time.Millisecond)

	h.ShowOverlay(socket)
	if !h.WaitForOverlayOn("HEADLESS-2", 10*time.Second) {
		t.Fatal("overlay never came back after its output was unplugged: " +
			"the layer surface was closed by the compositor and never rebuilt")
	}
}
