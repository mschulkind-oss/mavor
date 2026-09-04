//go:build integration

package integration

import (
	"bytes"
	"image/png"
	"testing"
)

// TestHarnessStartsAndScreenshots is the smoke test for the rig itself. If it
// fails, every other integration test is unreliable; fix this first.
func TestHarnessStartsAndScreenshots(t *testing.T) {
	h := Start(t, Options{Width: 1280, Height: 720})

	png_bytes := h.Grim()
	if len(png_bytes) < 100 {
		t.Fatalf("grim returned only %d bytes — not a PNG", len(png_bytes))
	}
	img, err := png.Decode(bytes.NewReader(png_bytes))
	if err != nil {
		t.Fatalf("PNG decode: %v", err)
	}
	if w := img.Bounds().Dx(); w != 1280 {
		t.Fatalf("screenshot width = %d, want 1280", w)
	}
	if hpx := img.Bounds().Dy(); hpx != 720 {
		t.Fatalf("screenshot height = %d, want 720", hpx)
	}
}
