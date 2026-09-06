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

// The waveform is a time history — one column per sample — so samples are the
// one thing that must NOT be collapsed to the newest.
//
// Keeping only the newest made the ring advance once per frame (37.5 ms) while
// the recorder samples every 30 ms: it scrolled slower than the audio, by a
// varying number of columns depending on how the two clocks lined up. That is
// what "a little bit of jitteriness in the waveform" was.
func TestEverySampleIsKeptForTheWaveform(t *testing.T) {
	o := &WL{done: make(chan struct{})}
	for i := 0; i < 5; i++ {
		if err := o.SetLevel(float64(i) / 10); err != nil {
			t.Fatal(err)
		}
	}
	var seen uint64
	got, changed := o.takeDesired(&seen)
	if !changed {
		t.Fatal("no change reported after five samples")
	}
	if len(got.levels) != 5 {
		t.Fatalf("kept %d samples, want all 5 — a dropped sample is a missing column", len(got.levels))
	}
	for i, lv := range got.levels {
		if want := float64(i) / 10; lv != want {
			t.Errorf("sample %d = %v, want %v — order matters in a time history", i, lv, want)
		}
	}
}

// Taking the batch must not hand the same samples out twice, or the waveform
// would scroll through them again on the next frame.
func TestSamplesAreConsumedOnce(t *testing.T) {
	o := &WL{done: make(chan struct{})}
	_ = o.SetLevel(0.4)
	var seen uint64
	if got, _ := o.takeDesired(&seen); len(got.levels) != 1 {
		t.Fatalf("first take got %d samples, want 1", len(got.levels))
	}
	if got, _ := o.takeDesired(&seen); len(got.levels) != 0 {
		t.Errorf("second take got %d samples, want none", len(got.levels))
	}
}

// The batch is bounded, and it is the OLDEST that go: a waveform is about what
// just happened.
func TestPendingSamplesAreBoundedKeepingTheNewest(t *testing.T) {
	o := &WL{done: make(chan struct{})}
	for i := 0; i < maxPendingLevels*4; i++ {
		_ = o.SetLevel(float64(i))
	}
	var seen uint64
	got, _ := o.takeDesired(&seen)
	if len(got.levels) > maxPendingLevels {
		t.Fatalf("kept %d samples, over the %d bound", len(got.levels), maxPendingLevels)
	}
	last := got.levels[len(got.levels)-1]
	if want := float64(maxPendingLevels*4 - 1); last != want {
		t.Errorf("newest sample = %v, want %v — the newest must survive", last, want)
	}
}
