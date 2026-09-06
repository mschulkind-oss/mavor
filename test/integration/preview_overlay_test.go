//go:build integration

// integration only, not e2e: the helpers these use (findBrightBands, rowBand,
// decodePNG) live in overlay_test.go, which is integration-tagged. A wider tag
// here compiles under e2e without them.

package integration

import (
	"bytes"
	"image/png"
	"io"
	"log/slog"
	"strings"
	"testing"

	"github.com/mschulkind-oss/mavor/internal/overlay"
)

// These exist because a run of overlay bugs reached a human that no unit test
// could have caught: the surface vanished mid-recording, the pill did not
// always appear, and the preview never drew. Every one of them lived in the
// interaction between the render loop and a real compositor — the layer that
// SceneSize and FitPreviewTail tests cannot see.
//
// The rule they encode: anything that decides whether pixels reach the screen
// gets a test that looks at the screen.

// litPixels counts non-background pixels, which on an empty headless desktop
// is a serviceable "is anything drawn" signal.
func litPixels(t *testing.T, h *Harness) int {
	t.Helper()
	img, err := png.Decode(bytes.NewReader(h.Grim()))
	if err != nil {
		t.Fatalf("decode screenshot: %v", err)
	}
	n := 0
	b := img.Bounds()
	for y := b.Min.Y; y < b.Max.Y; y++ {
		for x := b.Min.X; x < b.Max.X; x++ {
			r, g, bl, a := img.At(x, y).RGBA()
			if a>>8 > 8 && (r>>8 > 40 || g>>8 > 40 || bl>>8 > 40) {
				n++
			}
		}
	}
	return n
}

// overlayBand returns the row range of the overlay: waybar spans the whole
// screen and is always the first bright band, so the overlay is the last one.
// Measuring the union of every lit pixel measures waybar instead, which is a
// mistake this file made once already.
// With preview text the overlay is TWO bands — the pill and the strip below
// it, separated by previewGap — so this spans from the first non-waybar band
// to the last rather than returning one of them.
func overlayBand(t *testing.T, h *Harness) (rowBand, bool) {
	t.Helper()
	bands := findBrightBands(decodePNG(t, h.Grim()))
	if len(bands) < 2 {
		return rowBand{}, false
	}
	return rowBand{start: bands[1].start, end: bands[len(bands)-1].end}, true
}

// The pill must reach the screen at all. Trivial to state, and it is one of
// the things that stopped working.
func TestRecordingPillReachesTheScreen(t *testing.T) {
	h := sharedCompositor(t)
	t.Setenv("XDG_RUNTIME_DIR", h.XDGRuntime)
	t.Setenv("WAYLAND_DISPLAY", h.WaylandDisp)

	ov, err := overlay.NewDefault(testTopMargin, testPreviewWidth, slog.New(slog.NewTextHandler(io.Discard, nil)))
	if err != nil {
		t.Fatalf("overlay.NewDefault: %v", err)
	}
	defer ov.Close()

	empty := litPixels(t, h)
	if err := ov.Show(overlay.Recording); err != nil {
		t.Fatalf("Show(Recording): %v", err)
	}
	waitForOverlay(t, h)

	if got := litPixels(t, h); got <= empty {
		t.Fatalf("nothing drawn for Recording: %d lit pixels, was %d when hidden", got, empty)
	}
}

// Transcribing has its own colour and its own label, and "it doesn't always
// show up" was a real report. A state change must reach the screen.
func TestTranscribingReachesTheScreen(t *testing.T) {
	h := sharedCompositor(t)
	t.Setenv("XDG_RUNTIME_DIR", h.XDGRuntime)
	t.Setenv("WAYLAND_DISPLAY", h.WaylandDisp)

	ov, err := overlay.NewDefault(testTopMargin, testPreviewWidth, slog.New(slog.NewTextHandler(io.Discard, nil)))
	if err != nil {
		t.Fatalf("overlay.NewDefault: %v", err)
	}
	defer ov.Close()

	if err := ov.Show(overlay.Recording); err != nil {
		t.Fatal(err)
	}
	waitForOverlay(t, h)

	if err := ov.Show(overlay.Transcribing); err != nil {
		t.Fatalf("Show(Transcribing): %v", err)
	}
	waitForOverlay(t, h)

	if got := litPixels(t, h); got == 0 {
		t.Fatal("nothing on screen after switching to Transcribing")
	}
}

// The regression test for the overlay that vanished mid-recording.
//
// Setting preview text grows the surface, and a layer-shell resize is
// asynchronous — the frame that asks for the new size still has the old
// surface. That one frame stopped the render loop dead, and because the
// daemon records independently the user saw the overlay disappear while
// dictation carried on. Nothing survives that except drawing it for real.
func TestPreviewTextSurvivesTheResizeAndKeepsDrawing(t *testing.T) {
	h := sharedCompositor(t)
	t.Setenv("XDG_RUNTIME_DIR", h.XDGRuntime)
	t.Setenv("WAYLAND_DISPLAY", h.WaylandDisp)

	ov, err := overlay.NewDefault(testTopMargin, testPreviewWidth, slog.New(slog.NewTextHandler(io.Discard, nil)))
	if err != nil {
		t.Fatalf("overlay.NewDefault: %v", err)
	}
	defer ov.Close()

	if err := ov.Show(overlay.Recording); err != nil {
		t.Fatal(err)
	}
	waitForOverlay(t, h)
	pillOnly, ok := overlayBand(t, h)
	if !ok {
		t.Fatal("no overlay band on screen for Recording")
	}

	if err := ov.SetText("the quick brown fox jumps over the lazy dog"); err != nil {
		t.Fatalf("SetText: %v", err)
	}
	waitForOverlay(t, h)

	withPreview, ok := overlayBand(t, h)
	if !ok {
		t.Fatal("overlay disappeared once preview text was set")
	}
	pillH := pillOnly.end - pillOnly.start
	previewH := withPreview.end - withPreview.start
	if previewH <= pillH {
		t.Errorf("overlay did not grow for the preview: %d rows then %d", pillH, previewH)
	}

	// The loop must still be alive after the resize. Growing the text again
	// is what proves it: a stopped render loop leaves the last frame on
	// screen, so only a further change can tell the two apart.
	if err := ov.SetText(strings.Repeat("more words arriving ", 12)); err != nil {
		t.Fatalf("SetText (grown): %v", err)
	}
	waitForOverlay(t, h)

	if got := litPixels(t, h); got == 0 {
		t.Fatal("overlay went blank after the preview grew — the render loop stopped")
	}

	// And it must still respond to a state change, which is the strongest
	// available evidence that the loop is still draining its queue.
	if err := ov.Show(overlay.Transcribing); err != nil {
		t.Fatalf("Show after preview: %v — the render loop is gone", err)
	}
	waitForOverlay(t, h)
	if got := litPixels(t, h); got == 0 {
		t.Fatal("nothing drawn after the post-preview state change")
	}
}

// The width cap is the thing standing between a long dictation and an overlay
// wider than the screen, and it is measured here in real pixels rather than in
// SceneSize's arithmetic.
func TestPreviewStaysWithinItsShareOfTheScreen(t *testing.T) {
	h := sharedCompositor(t)
	t.Setenv("XDG_RUNTIME_DIR", h.XDGRuntime)
	t.Setenv("WAYLAND_DISPLAY", h.WaylandDisp)

	ov, err := overlay.NewDefault(testTopMargin, testPreviewWidth, slog.New(slog.NewTextHandler(io.Discard, nil)))
	if err != nil {
		t.Fatalf("overlay.NewDefault: %v", err)
	}
	defer ov.Close()

	if err := ov.Show(overlay.Recording); err != nil {
		t.Fatal(err)
	}
	if err := ov.SetText(strings.Repeat("an extremely long dictation that never stops ", 40)); err != nil {
		t.Fatal(err)
	}
	waitForOverlay(t, h)

	img := decodePNG(t, h.Grim())
	screen := img.Bounds().Dx()

	// Waybar spans the whole screen and is the first bright band; the overlay
	// is the one below it. Measuring the union of every lit pixel would be
	// measuring waybar, which is how this test first "failed".
	bands := findBrightBands(img)
	if len(bands) < 2 {
		t.Fatalf("expected waybar and overlay bands, got %v", bands)
	}
	band := bands[len(bands)-1]

	widest := 0
	for y := band.start; y <= band.end && y < img.Bounds().Max.Y; y++ {
		minX, maxX := -1, -1
		for x := img.Bounds().Min.X; x < img.Bounds().Max.X; x++ {
			r, g, b, a := img.At(x, y).RGBA()
			if a>>8 > 8 && (r>>8 > 40 || g>>8 > 40 || b>>8 > 40) {
				if minX < 0 {
					minX = x
				}
				maxX = x
			}
		}
		if minX >= 0 && maxX-minX+1 > widest {
			widest = maxX - minX + 1
		}
	}

	// testPreviewWidth of the screen, plus slack for rounding and the strip's
	// own padding. The point is that it is bounded, not that it is exact.
	limit := int(float64(screen)*testPreviewWidth) + 40
	if widest > limit {
		t.Errorf("overlay drew %dpx wide on a %dpx screen, over the %dpx budget", widest, screen, limit)
	}
	if widest == 0 {
		t.Error("no overlay pixels found in the lower band")
	}
}
