package main

import "testing"

// The cadence arithmetic is tested here rather than through streamOnce
// because streamOnce needs a downloaded model and a working ONNX runtime, and
// the numbers that matter are pure bookkeeping over a sequence of chunks.
// Every one of the cases below is a way to produce a plausible-looking figure
// that is wrong.

// feed drives a tracker with one entry per chunk: text returned, and how long
// the call took. Chunks are spaced streamChunkMS apart, the way the feed loop
// spaces them.
func feed(t *testing.T, texts []string, callMS []float64) *cadenceTracker {
	t.Helper()
	c := newCadenceTracker()
	for i, text := range texts {
		ms := 0.0
		if i < len(callMS) {
			ms = callMS[i]
		}
		c.observe(float64((i+1)*streamChunkMS), text, ms)
	}
	return c
}

func TestCadenceTrackerCountsRepaintsAndTheirSpacing(t *testing.T) {
	// Ten chunks at 30 ms: silence, then text growing three times.
	texts := []string{"", "", "", "a", "a", "a b", "a b", "a b", "a b c", "a b c"}
	got := feed(t, texts, nil).finish(300)

	if got.Updates != 3 {
		t.Errorf("Updates = %d, want 3 — an update is a chunk whose text differs from the last one", got.Updates)
	}
	// Updates land at 120, 180 and 270 ms of audio; the tail runs to 300.
	if got.MaxGapMS != 90 {
		t.Errorf("MaxGapMS = %.0f, want 90", got.MaxGapMS)
	}
	if got.MeanGapMS != 60 {
		t.Errorf("MeanGapMS = %.0f, want 60 ((60+90+30)/3)", got.MeanGapMS)
	}
}

func TestCadenceTrackerExcludesTheWaitBeforeTheFirstUpdate(t *testing.T) {
	// A model that says nothing for the first two seconds and then paints
	// steadily is slow to start, not lumpy. Time to first token is the column
	// that reports the former; folding it in here would report it twice and
	// would make a perfectly smooth preview look stuttery.
	var texts []string
	for i := 0; i < 66; i++ {
		texts = append(texts, "") // ~2 s of nothing
	}
	texts = append(texts, "a", "a b", "a b c")
	got := feed(t, texts, nil).finish(float64(len(texts) * streamChunkMS))

	if got.Updates != 3 {
		t.Fatalf("Updates = %d, want 3", got.Updates)
	}
	if got.MaxGapMS != 30 {
		t.Errorf("MaxGapMS = %.0f, want 30 — the silence before the first update is not a gap", got.MaxGapMS)
	}
}

func TestCadenceTrackerCountsTheSilenceAfterTheLastUpdate(t *testing.T) {
	// The failure this catches: a model emits two partials in the first
	// tenth of a second, goes dark for the rest of the utterance, and hands
	// everything over at StopStream. Counting only the intervals between
	// updates would score that a flawless 30 ms mean — a perfect preview
	// that in fact showed nothing for five seconds.
	texts := []string{"a", "a b"}
	for i := 0; i < 100; i++ {
		texts = append(texts, "a b")
	}
	got := feed(t, texts, nil).finish(float64(len(texts) * streamChunkMS))

	if got.Updates != 2 {
		t.Fatalf("Updates = %d, want 2", got.Updates)
	}
	if got.MaxGapMS < 3000 {
		t.Errorf("MaxGapMS = %.0f, want the whole silent tail (~3 s) — a preview that stops updating must not score clean", got.MaxGapMS)
	}
}

func TestCadenceTrackerReportsNoGapsWhenNothingEverAppears(t *testing.T) {
	// A recognizer that accepts every chunk and returns nothing has no
	// intervals to average. Zero has to mean "never updated" rather than
	// "updated instantly", which is why the report renders a dash here.
	got := feed(t, []string{"", "", ""}, nil).finish(90)
	if got.Updates != 0 || got.MeanGapMS != 0 || got.MaxGapMS != 0 {
		t.Errorf("got %+v, want a zeroed cadence for a stream that never produced text", got)
	}
}

func TestCadenceTrackerCountsTextClearingAsARepaint(t *testing.T) {
	// A recognizer that resets its hypothesis at an endpoint blanks the
	// overlay. That is something the user sees, so it counts.
	got := feed(t, []string{"a", "", "b"}, nil).finish(90)
	if got.Updates != 3 {
		t.Errorf("Updates = %d, want 3 — clearing the text is a repaint too", got.Updates)
	}
}

func TestCadenceTrackerMeasuresTheSlowestCallAgainstTheDaemonsTick(t *testing.T) {
	// Wall clock, unlike the gaps: this is the number that decides whether
	// the daemon's preview goroutine can keep up with its own ticker.
	got := feed(t, []string{"a", "a b", "a b c", "a b c d"}, []float64{5, 40, 12, 100}).finish(120)
	if got.SlowestChunkMS != 100 {
		t.Errorf("SlowestChunkMS = %.0f, want 100", got.SlowestChunkMS)
	}
	if got.SlowChunks != 2 {
		t.Errorf("SlowChunks = %d, want 2 (the 40 ms and 100 ms calls both overran the %d ms tick)",
			got.SlowChunks, streamChunkMS)
	}
}
