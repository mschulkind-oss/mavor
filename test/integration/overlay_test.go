//go:build integration

package integration

import (
	"bytes"
	"image"
	"image/png"
	"testing"
	"time"

	"github.com/mschulkind-oss/mavor/internal/ipc"
	"github.com/mschulkind-oss/mavor/internal/overlay"
)

// TestOverlayDoesNotOverlapWaybar starts a real headless Sway with waybar on
// the top, runs the daemon, sends a toggle to bring the overlay up, and
// pixel-asserts that the overlay's top edge is strictly below waybar's
// bottom edge.
func TestOverlayDoesNotOverlapWaybar(t *testing.T) {
	h := Start(t, Options{
		Width:        testWidth,
		Height:       testHeight,
		LaunchWaybar: true,
	})
	socket, _ := h.RunDaemon(t.Context(), MavorBinary, "tiny.en")

	// Toggle into Recording so the overlay shows.
	if _, err := ipc.Send(socket, ipc.Request{Action: "toggle"}, 2*time.Second); err != nil {
		t.Fatalf("toggle: %v", err)
	}
	// Confirm the daemon actually entered Recording — if parec fails the
	// FSM stays in Recording (failure events are ignored from that state).
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		if r, err := ipc.Send(socket, ipc.Request{Action: "status"}, 500*time.Millisecond); err == nil && r.State == "recording" {
			break
		}
		time.Sleep(20 * time.Millisecond)
	}
	// Allow the GTK overlay a moment to map and render.
	time.Sleep(400 * time.Millisecond)

	img := decodePNG(t, h.Grim())
	if img.Bounds().Dx() != testWidth || img.Bounds().Dy() != testHeight {
		t.Fatalf("screenshot %dx%d, want %dx%d",
			img.Bounds().Dx(), img.Bounds().Dy(), testWidth, testHeight)
	}

	bands := findBrightBands(img)
	t.Logf("bright row bands: %v", bands)
	if len(bands) < 2 {
		t.Fatalf("expected ≥2 bright row bands (waybar + overlay), got %v", bands)
	}

	waybar := bands[0]
	overlay := bands[1]
	if waybar.start != 0 {
		t.Errorf("waybar band starts at row %d, want 0", waybar.start)
	}
	if waybar.end+1 > overlay.start {
		t.Fatalf("overlay band starts at row %d but waybar ends at %d — they overlap",
			overlay.start, waybar.end)
	}
	if gap := overlay.start - (waybar.end + 1); gap < testTopMargin/2 {
		t.Errorf("gap between waybar and overlay is %d rows, want at least %d (half of top_margin)", gap, testTopMargin/2)
	}
}

// TestOverlayWithoutWaybarStillFloats sanity-checks the overlay still renders
// when no waybar is present. Catches the case where the overlay is silently
// invisible due to a layer-shell setup mistake.
func TestOverlayWithoutWaybarStillFloats(t *testing.T) {
	h := Start(t, Options{Width: testWidth, Height: testHeight})
	socket, _ := h.RunDaemon(t.Context(), MavorBinary, "tiny.en")

	if _, err := ipc.Send(socket, ipc.Request{Action: "toggle"}, 2*time.Second); err != nil {
		t.Fatalf("toggle: %v", err)
	}
	time.Sleep(400 * time.Millisecond)

	bands := findBrightBands(decodePNG(t, h.Grim()))
	if len(bands) < 1 {
		t.Fatalf("expected at least one bright row band (overlay), got %v", bands)
	}
	if bands[0].start < testTopMargin/2 {
		t.Errorf("overlay band starts at row %d, want ≥ %d (half of top_margin)",
			bands[0].start, testTopMargin/2)
	}
}

// TestWaveformRingScrolls feeds a known level sequence through the real GTK
// overlay and asserts the time-history ring scrolls: index 0 is the oldest
// retained sample, the last index is the newest, and once the ring is full
// the oldest sample falls off the left edge. Regression for the "only the
// live column moved" bug where the shift kept index 0 pinned and overwrote
// the tail instead of scrolling.
func TestWaveformRingScrolls(t *testing.T) {
	// Shared, not per-test: this test builds an overlay in-process, and the
	// GTK application it starts must not outlive its compositor.
	h := sharedCompositor(t)
	t.Setenv("XDG_RUNTIME_DIR", h.XDGRuntime)
	t.Setenv("WAYLAND_DISPLAY", h.WaylandDisp)
	t.Setenv("DBUS_SESSION_BUS_ADDRESS", h.DBusAddr)
	t.Setenv("GDK_BACKEND", "wayland")

	ov, err := overlay.NewGTK(testTopMargin)
	if err != nil {
		t.Fatalf("overlay.NewGTK: %v", err)
	}
	defer ov.Close()

	// Map the window (as the daemon does on Show(Recording)) so Close's
	// window.Destroy runs against a realized surface.
	if err := ov.Show(overlay.Recording); err != nil {
		t.Fatalf("Show(Recording): %v", err)
	}

	const ringLen = 46
	// Feed ringLen+2 values so the ring is full and the first two fall off.
	for i := 0; i < ringLen+2; i++ {
		if err := ov.SetLevel(float64(i+1) / 100.0); err != nil {
			t.Fatalf("SetLevel: %v", err)
		}
	}

	wave := ov.Wave()
	if len(wave) != ringLen {
		t.Fatalf("Wave() len = %d, want %d", len(wave), ringLen)
	}
	for i, v := range wave {
		want := float64(i+3) / 100.0 // 0.01 and 0.02 fell off the left edge
		if v != want {
			t.Fatalf("wave[%d] = %v, want %v (ring: %v)", i, v, want, wave)
		}
	}
}

type rowBand struct{ start, end int }

// findBrightBands scans the image and returns contiguous row ranges whose
// brightness sum exceeds a threshold. Used to identify visually-rendered
// elements (waybar, overlay) on an otherwise-empty headless sway desktop.
func findBrightBands(img image.Image) []rowBand {
	const minBrightRowsForBand = 3
	const minPxForBrightRow = 20 // require at least N non-background pixels

	bg := backgroundLuma(img)
	threshold := bg + 30 // 30/255 brighter than background == "rendered content"

	var bands []rowBand
	cur := rowBand{start: -1, end: -1}
	flush := func() {
		if cur.start >= 0 && cur.end-cur.start+1 >= minBrightRowsForBand {
			bands = append(bands, cur)
		}
		cur = rowBand{start: -1, end: -1}
	}
	for y := img.Bounds().Min.Y; y < img.Bounds().Max.Y; y++ {
		bright := 0
		for x := img.Bounds().Min.X; x < img.Bounds().Max.X; x++ {
			if luma(img.At(x, y)) > threshold {
				bright++
			}
		}
		if bright >= minPxForBrightRow {
			if cur.start < 0 {
				cur.start = y
			}
			cur.end = y
		} else {
			flush()
		}
	}
	flush()
	return bands
}

// backgroundLuma samples the bottom-right corner — far from any expected
// rendered element — and returns its luma as a baseline.
func backgroundLuma(img image.Image) int {
	x := img.Bounds().Max.X - 4
	y := img.Bounds().Max.Y - 4
	return luma(img.At(x, y))
}

func luma(c interface{ RGBA() (r, g, b, a uint32) }) int {
	r, g, b, _ := c.RGBA()
	// 8-bit luma using rec601 weights.
	return int((299*r + 587*g + 114*b) / 1000 / 256)
}

func decodePNG(t *testing.T, b []byte) image.Image {
	t.Helper()
	img, err := png.Decode(bytes.NewReader(b))
	if err != nil {
		t.Fatalf("png decode: %v (len=%d)", err, len(b))
	}
	return img
}
