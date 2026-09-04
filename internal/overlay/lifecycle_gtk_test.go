//go:build cgo && !nogtk

package overlay

import (
	"os"
	"testing"
)

// Creating a second overlay in one process used to terminate the binary: each
// NewGTK built its own gtk.Application, and GTK does not support being torn
// down and re-initialized. The integration suite hit this as a whole-suite
// failure that no single test reproduced.
//
// Needs a compositor, so it is skipped where there is no Wayland display.
func TestNewGTKIsReusableWithinOneProcess(t *testing.T) {
	if os.Getenv("WAYLAND_DISPLAY") == "" {
		t.Skip("no WAYLAND_DISPLAY; overlay construction needs a compositor")
	}

	first, err := NewGTK(8)
	if err != nil {
		t.Fatalf("first NewGTK: %v", err)
	}
	if err := first.Show(Recording); err != nil {
		t.Fatalf("first Show: %v", err)
	}
	if err := first.Close(); err != nil {
		t.Fatalf("first Close: %v", err)
	}

	// The second construction is the regression: before the shared
	// application, reaching this line at all was the failure mode.
	second, err := NewGTK(8)
	if err != nil {
		t.Fatalf("second NewGTK after closing the first: %v", err)
	}
	defer second.Close()

	if err := second.Show(Recording); err != nil {
		t.Fatalf("second Show: %v", err)
	}
	if err := second.SetLevel(0.5); err != nil {
		t.Fatalf("second SetLevel: %v", err)
	}
	if w := second.Wave(); len(w) != waveCols {
		t.Errorf("second overlay Wave() len = %d, want %d", len(w), waveCols)
	}
}
