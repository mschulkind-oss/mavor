package overlay

import (
	"testing"
	"time"
)

// The producers write what they want and return. The render loop reads the
// newest value each frame. These pin the two properties that matters:
// a producer never blocks, and a state change is never lost.

func TestStateChangeSurvivesAFloodOfLevels(t *testing.T) {
	o := &WL{done: make(chan struct{})}

	// Far more updates than any queue would hold. The old code dropped these
	// on a full channel — including the Show, which is how a transcribing
	// state went missing on screen.
	for i := 0; i < 10_000; i++ {
		if err := o.SetLevel(float64(i%100) / 100); err != nil {
			t.Fatalf("SetLevel: %v", err)
		}
	}
	if err := o.Show(Transcribing); err != nil {
		t.Fatalf("Show: %v", err)
	}
	for i := 0; i < 10_000; i++ {
		if err := o.SetLevel(0.5); err != nil {
			t.Fatalf("SetLevel: %v", err)
		}
	}

	var seen uint64
	got, changed := o.takeDesired(&seen)
	if !changed {
		t.Fatal("nothing reported as changed after 20,000 updates")
	}
	if got.visual != Transcribing {
		t.Errorf("visual = %v, want %v — the state change was lost", got.visual, Transcribing)
	}
}

// Only the newest value has meaning, so an overwritten one is not a loss.
func TestOnlyTheNewestValueIsKept(t *testing.T) {
	o := &WL{done: make(chan struct{})}
	for _, s := range []string{"one", "two", "three"} {
		if err := o.SetText(s); err != nil {
			t.Fatal(err)
		}
	}
	var seen uint64
	got, _ := o.takeDesired(&seen)
	if got.preview != "three" {
		t.Errorf("preview = %q, want the newest %q", got.preview, "three")
	}
}

// A second read with nothing written in between must report no change, or the
// loop would repaint every frame for no reason.
func TestUnchangedStateReportsNoChange(t *testing.T) {
	o := &WL{done: make(chan struct{})}
	if err := o.Show(Recording); err != nil {
		t.Fatal(err)
	}
	var seen uint64
	if _, changed := o.takeDesired(&seen); !changed {
		t.Fatal("first read after a write reported no change")
	}
	if _, changed := o.takeDesired(&seen); changed {
		t.Error("second read with no writes reported a change")
	}
}

// Changing state clears the preview: the text belonged to the state being left.
func TestStateChangeClearsThePreview(t *testing.T) {
	o := &WL{done: make(chan struct{})}
	_ = o.Show(Recording)
	_ = o.SetText("half a sentence")
	_ = o.Show(Transcribing)

	var seen uint64
	got, _ := o.takeDesired(&seen)
	if got.preview != "" {
		t.Errorf("preview = %q, want it cleared by the state change", got.preview)
	}
}

// Producers must not block, whatever the render loop is doing — there is no
// loop at all here, which is the harshest version of that.
func TestProducersNeverBlockWithNoRenderLoop(t *testing.T) {
	o := &WL{done: make(chan struct{})}
	done := make(chan struct{})
	go func() {
		defer close(done)
		for i := 0; i < 50_000; i++ {
			_ = o.SetLevel(0.5)
			_ = o.SetText("x")
			_ = o.Show(Recording)
		}
	}()
	select {
	case <-done:
	case <-time.After(10 * time.Second):
		t.Fatal("producers blocked with no render loop draining")
	}
}
