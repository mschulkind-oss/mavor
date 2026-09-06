//go:build integration

package integration

import (
	"io"
	"log/slog"
	"testing"
	"time"

	"github.com/mschulkind-oss/mavor/internal/overlay"
)

// The regression test for "the overlay isn't showing up". It reproduced that
// in about two seconds with no daemon and no audio, and it is what found the
// cause: the render loop died on the SECOND hide-and-show cycle with a broken
// pipe and no protocol error to say why.
//
// Every dictation hides and shows the overlay, so every dictation after the
// first has been running on a dead loop. The other overlay tests all show one
// state, assert, and stop — which is why this went unnoticed through several
// rounds of overlay changes.
//
// It asserts both halves, because each was broken in turn: that showing puts
// the overlay on screen, and that hiding takes it off. The first fix made the
// surface survive by never unmapping, which left it permanently visible — a
// test that only checked survival would have called that a pass.
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
		shown := lit()
		if shown == 0 {
			t.Fatalf("cycle %d: nothing on screen while recording", cycle)
		}

		if err := ov.Show(overlay.Hidden); err != nil {
			t.Fatalf("cycle %d Show(Hidden): %v", cycle, err)
		}
		time.Sleep(300 * time.Millisecond)
		hidden := lit()
		t.Logf("cycle %d: recording lit=%d, hidden lit=%d", cycle, shown, hidden)
		// Hiding has to actually hide. Drawing a transparent frame instead
		// of unmapping is only correct if nothing shows through it.
		if hidden >= shown {
			t.Errorf("cycle %d: hidden shows %d lit rows against %d while recording — it is not hidden",
				cycle, hidden, shown)
		}
	}
}
