package overlay

import (
	"testing"
	"time"
)

// newProducerSide builds just enough of a WL to exercise the producer-facing
// setters. The render loop is not running: these tests are about what the
// setters signal, not what the loop draws.
func newProducerSide() *WL {
	return &WL{
		quit: make(chan struct{}),
		done: make(chan struct{}),
		err:  make(chan error, 1),
		wake: make(chan struct{}, 1),
	}
}

func woken(o *WL) bool {
	select {
	case <-o.wake:
		return true
	default:
		return false
	}
}

// The render loop ticks slowly while nothing is on screen, which is almost
// all of a daemon's life. That is only safe because Show wakes it: otherwise
// the pill would take up to idleInterval to appear after the keybind.
func TestShowWakesTheRenderLoop(t *testing.T) {
	o := newProducerSide()
	if err := o.Show(Recording); err != nil {
		t.Fatal(err)
	}
	if !woken(o) {
		t.Error("Show did not signal the render loop: the overlay would not appear until the next idle tick")
	}
}

// The invariant that makes the wake channel safe, and the reason SetLevel
// must never signal it.
//
// The waveform advances EXACTLY ONE column per loop iteration — that is what
// makes its motion even, and it was the fix for a stutter the user reported.
// The recorder samples every 30ms and the loop paints every 37.5ms. If a
// level sample woke the loop, the loop would run on the sampler's clock, the
// ring would advance at 33Hz instead of 26.7Hz, and the scroll would be
// uneven again in exactly the old way.
func TestLevelSamplesDoNotWakeTheRenderLoop(t *testing.T) {
	o := newProducerSide()
	for i := 0; i < 20; i++ {
		if err := o.SetLevel(0.5); err != nil {
			t.Fatal(err)
		}
	}
	if woken(o) {
		t.Error("SetLevel woke the render loop: the waveform will scroll at the recorder's rate, not the frame rate")
	}
}

// Preview text does not wake it either. While recording the loop is already
// at the frame rate, so text arrives within one frame; while hidden there is
// nothing to show it on.
func TestPreviewTextDoesNotWakeTheRenderLoop(t *testing.T) {
	o := newProducerSide()
	if err := o.SetText("some partial text"); err != nil {
		t.Fatal(err)
	}
	if woken(o) {
		t.Error("SetText woke the render loop")
	}
}

// Show is called from the daemon's state transitions and must never block on
// a loop that is busy or gone. The channel holds one signal; further Shows
// coalesce into it, which is correct because the loop reads the latest state
// rather than a queue of edits.
func TestRepeatedShowsCoalesceRatherThanBlock(t *testing.T) {
	o := newProducerSide()
	done := make(chan struct{})
	go func() {
		defer close(done)
		for i := 0; i < 100; i++ {
			_ = o.Show(Recording)
			_ = o.Show(Hidden)
		}
	}()
	select {
	case <-done:
	case <-time.After(2 * time.Second):
		t.Fatal("Show blocked: a full wake channel must not stall the daemon's state transitions")
	}
}

// The two rates, stated as a test so the relationship is checked rather than
// assumed: idle must be slower than a frame, or it is not saving anything,
// and it must stay short enough to keep the compositor's events drained.
func TestTheIdleRateIsSlowerThanAFrameButStillDrains(t *testing.T) {
	if idleInterval <= frameInterval {
		t.Errorf("idleInterval %v is not slower than frameInterval %v, so an idle daemon wakes as often as an animating one",
			idleInterval, frameInterval)
	}
	if idleInterval > time.Second {
		t.Errorf("idleInterval %v is long enough to risk the compositor's socket filling with unread events", idleInterval)
	}
}
