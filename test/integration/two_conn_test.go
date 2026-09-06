//go:build integration

package integration

import (
	"io"
	"log/slog"
	"testing"
	"time"

	"github.com/mschulkind-oss/mavor/internal/output"
	"github.com/mschulkind-oss/mavor/internal/overlay"
)

// The daemon holds two Wayland connections: the overlay's and the one
// in-process typing opens. Each is fine alone. This is the pair.
func TestOverlayAndTypingConnectionsCoexist(t *testing.T) {
	h := sharedCompositor(t)
	t.Setenv("XDG_RUNTIME_DIR", h.XDGRuntime)
	t.Setenv("WAYLAND_DISPLAY", h.WaylandDisp)
	quiet := slog.New(slog.NewTextHandler(io.Discard, nil))

	n, err := output.NewNative(quiet)
	if err != nil {
		t.Fatalf("NewNative: %v", err)
	}
	defer n.Close()

	ov, err := overlay.NewDefault(testTopMargin, testPreviewWidth, quiet)
	if err != nil {
		t.Fatalf("overlay.NewDefault: %v", err)
	}
	defer ov.Close()

	if err := ov.Show(overlay.Recording); err != nil {
		t.Fatal(err)
	}

	// Two seconds is well past where the daemon dies (about 1.2 s in).
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		if err := ov.SetLevel(0.5); err != nil {
			t.Fatalf("SetLevel after %v: %v", time.Since(deadline), err)
		}
		time.Sleep(30 * time.Millisecond)
	}

	if err := ov.Show(overlay.Transcribing); err != nil {
		t.Fatalf("Show after two seconds: %v — the overlay's loop died", err)
	}
	bands := findBrightBands(decodePNG(t, h.Grim()))
	t.Logf("bands after two seconds: %v", bands)
	if len(bands) == 0 {
		t.Error("nothing on screen after two seconds with both connections open")
	}
}
