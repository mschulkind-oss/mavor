package audio

import (
	"math"
	"math/rand"
	"runtime"
	"testing"
	"time"
)

// writeMixedWAV writes a recording whose loud and quiet stretches alternate,
// so the frame count DetectSpeech arrives at is a real number rather than
// "all of it" or "none of it".
func writeMixedWAV(t *testing.T, seconds int) (string, []int16) {
	t.Helper()
	path := t.TempDir() + "/mixed.wav"
	n := DefaultSampleRate * seconds
	samples := make([]int16, n)
	rng := rand.New(rand.NewSource(1))
	for i := range samples {
		// Half-second bands, alternating well above and well below the
		// threshold so a one-frame disagreement would show up.
		loud := (i/(DefaultSampleRate/2))%2 == 0
		amp := 40.0
		if loud {
			amp = 6000.0
		}
		samples[i] = int16(amp * math.Sin(float64(i)/12) * (0.9 + 0.2*rng.Float64()))
	}
	if err := WriteWAV(path, samples, DefaultSampleRate); err != nil {
		t.Fatal(err)
	}
	return path, samples
}

// DetectSpeech no longer reads the file into a slice, so the thing to prove is
// that streaming it a frame at a time reaches the same verdict the slice did.
// The reference is SpeechDuration over the fully-materialized samples, which
// is what DetectSpeech used to call.
func TestStreamedDetectionMatchesTheWholeFileScan(t *testing.T) {
	path, samples := writeMixedWAV(t, 4)

	want := SpeechDuration(samples, DefaultSampleRate, SpeechRMSThreshold)
	if want <= 0 || want >= 4*time.Second {
		t.Fatalf("fixture is degenerate: %v of speech in 4s", want)
	}

	// Sweep the threshold across the answer, including the exact boundary,
	// where an off-by-one frame between the two implementations would show.
	for _, minSpeech := range []time.Duration{
		0, 30 * time.Millisecond, want - 30*time.Millisecond, want,
		want + 30*time.Millisecond, 4 * time.Second,
	} {
		got, err := DetectSpeech(path, minSpeech)
		if err != nil {
			t.Fatalf("DetectSpeech(%v): %v", minSpeech, err)
		}
		if expect := want >= minSpeech; got != expect {
			t.Errorf("DetectSpeech(minSpeech=%v) = %v, want %v (file holds %v of speech)",
				minSpeech, got, expect, want)
		}
	}
}

// The invariant, and the reason for the change: deciding whether a recording
// contains speech must not cost memory proportional to the recording. It used
// to allocate the PCM bytes and then the same samples again as int16 — 7.7MB
// for a two-minute dictation — on the path between the key being released and
// Transcribe being called.
func TestDetectSpeechDoesNotScaleWithTheRecording(t *testing.T) {
	perCall := func(seconds int) uint64 {
		path, _ := writeMixedWAV(t, seconds)
		// A minSpeech nothing can satisfy forces the whole file to be scanned;
		// otherwise the early return makes the two lengths trivially equal.
		unreachable := time.Duration(seconds*2) * time.Second

		// Bytes, not allocation count. The old implementation made exactly
		// eight allocations at every length too — they just got bigger with
		// the recording, which is the whole defect. A count-based assertion
		// passes on the bug it is meant to catch.
		const calls = 3
		var before, after runtime.MemStats
		runtime.GC()
		runtime.ReadMemStats(&before)
		for i := 0; i < calls; i++ {
			if _, err := DetectSpeech(path, unreachable); err != nil {
				t.Fatal(err)
			}
		}
		runtime.ReadMemStats(&after)
		return (after.TotalAlloc - before.TotalAlloc) / calls
	}

	const short, long = 2, 30
	small, large := perCall(short), perCall(long)
	t.Logf("bytes per call: %d at %ds, %d at %ds", small, short, large, long)

	if large > small*3/2 {
		t.Errorf("a %ds recording costs %d B against %d B for %ds: "+
			"the scan is holding the file rather than a frame of it", long, large, small, short)
	}
}
