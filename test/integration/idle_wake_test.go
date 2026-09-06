//go:build integration

package integration

import (
	"io"
	"log/slog"
	"testing"
	"time"

	"github.com/mschulkind-oss/mavor/internal/overlay"
)

// The render loop drops to a slow tick while nothing is on screen, which is
// almost all of a daemon's life — it used to wake 27 times a second forever,
// each time blocking a millisecond in a read for events that were not coming.
//
// The risk that trades against is latency: press the keybind after an hour of
// idling and the pill must appear at once, not at the next idle tick. Show
// signals the loop directly for exactly that reason, and this measures it
// against a real compositor rather than trusting the channel.
// idleTick mirrors overlay.idleInterval, which is unexported.
const idleTick = 500 * time.Millisecond

func TestTheOverlayAppearsAtOnceAfterIdling(t *testing.T) {
	h := sharedCompositor(t)
	t.Setenv("XDG_RUNTIME_DIR", h.XDGRuntime)
	t.Setenv("WAYLAND_DISPLAY", h.WaylandDisp)

	ov, err := overlay.NewDefault(testTopMargin, testPreviewWidth, slog.New(slog.NewTextHandler(io.Discard, nil)))
	if err != nil {
		t.Fatal(err)
	}
	defer ov.Close()

	// Rows of rendered content, waybar's own bar included — so the test
	// works against a baseline rather than against zero.
	lit := func() int {
		n := 0
		for _, b := range findBrightBands(decodePNG(t, h.Grim())) {
			n += b.end - b.start
		}
		return n
	}

	// Calibrate: a screenshot is not free, and the poll below can only
	// notice the overlay one screenshot at a time. That cost is the floor of
	// anything this test can measure, so the ceiling is expressed relative
	// to it rather than as a bare number that would be a coin flip on a
	// slower machine.
	if err := ov.Show(overlay.Hidden); err != nil {
		t.Fatal(err)
	}
	time.Sleep(1500 * time.Millisecond) // several idle ticks: the loop is settled
	shotStart := time.Now()
	baseline := lit()
	shotCost := time.Since(shotStart)

	// The worst of several cycles, not the best. If Show waited for the idle
	// tick, the wait would vary from nothing to a full interval depending on
	// where the tick happened to fall — so a single trial, or a minimum over
	// trials, could pass on the broken behaviour by luck. The maximum cannot.
	var worst time.Duration
	for cycle := 1; cycle <= 4; cycle++ {
		start := time.Now()
		if err := ov.Show(overlay.Recording); err != nil {
			t.Fatal(err)
		}

		appeared := time.Duration(0)
		deadline := time.Now().Add(3 * time.Second)
		for time.Now().Before(deadline) {
			if lit() > baseline {
				appeared = time.Since(start)
				break
			}
			time.Sleep(10 * time.Millisecond)
		}
		if appeared == 0 {
			t.Fatalf("cycle %d: the overlay never appeared after Show(Recording)", cycle)
		}
		if appeared > worst {
			worst = appeared
		}

		if err := ov.Show(overlay.Hidden); err != nil {
			t.Fatal(err)
		}
		// Long enough for the loop to drop back to the idle rate and for the
		// next Show to land at an arbitrary point within that interval.
		time.Sleep(700 * time.Millisecond)
	}

	// Two screenshots is the realistic floor: the first poll fires before the
	// loop has woken, painted and committed, so the appearance is normally
	// caught on the second. Waiting for an idle tick instead would add up to
	// another 500ms on top, which this still separates cleanly.
	ceiling := 2*shotCost + 150*time.Millisecond
	t.Logf("worst appearance over 4 cycles: %v (one screenshot costs %v, idle tick is %v, ceiling %v)",
		worst, shotCost, idleTick, ceiling)

	if worst > ceiling {
		t.Errorf("the overlay took %v to appear at worst, over the %v ceiling — Show is waiting for the "+
			"idle tick (%v) instead of waking the render loop", worst, ceiling, idleTick)
	}
}
