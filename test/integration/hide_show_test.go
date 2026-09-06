//go:build integration

package integration

import (
	"io"
	"log/slog"
	"testing"
	"time"

	"github.com/mschulkind-oss/mavor/internal/overlay"
)

// KNOWN FAILING. This reproduces, in about two seconds and with no daemon and
// no audio, the bug behind "the overlay isn't showing up": the render loop
// dies on the SECOND hide-and-show cycle with a broken pipe, and the
// compositor logs no protocol error to say why.
//
// Every dictation hides and shows the overlay, so every dictation after the
// first has been running on a dead loop. The other overlay tests all show one
// state, assert, and stop — which is why this went unnoticed through several
// rounds of overlay changes.
//
// It is committed failing on purpose. A reproducer that runs in two seconds is
// worth more than the same bug found again from a screenshot, and marking it
// skipped would hide exactly the thing that needs fixing.
//
// Confirmed present at dd5cef7 and 0b6f0d8 as well, so it is not a regression
// from the fixed-surface work or from in-process typing — it is older than
// both and was simply never exercised.
func TestOverlaySurvivesHideAndShowAgain(t *testing.T) {
	h := sharedCompositor(t)
	t.Setenv("XDG_RUNTIME_DIR", h.XDGRuntime)
	t.Setenv("WAYLAND_DISPLAY", h.WaylandDisp)

	ov, err := overlay.NewDefault(testTopMargin, testPreviewWidth, slog.New(slog.NewTextHandler(io.Discard, nil)))
	if err != nil {
		t.Fatal(err)
	}
	defer ov.Close()

	lit := func() int {
		bands := findBrightBands(decodePNG(t, h.Grim()))
		n := 0
		for _, b := range bands {
			n += b.end - b.start
		}
		return n
	}

	for cycle := 1; cycle <= 3; cycle++ {
		if err := ov.Show(overlay.Recording); err != nil {
			t.Fatalf("cycle %d Show(Recording): %v", cycle, err)
		}
		time.Sleep(300 * time.Millisecond)
		if lit() == 0 {
			t.Fatalf("cycle %d: nothing on screen while recording", cycle)
		}

		if err := ov.Show(overlay.Hidden); err != nil {
			t.Fatalf("cycle %d Show(Hidden): %v", cycle, err)
		}
		time.Sleep(300 * time.Millisecond)
		t.Logf("cycle %d: hidden lit=%d", cycle, lit())
	}
}
