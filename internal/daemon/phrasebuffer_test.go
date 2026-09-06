package daemon

import (
	"encoding/binary"
	"runtime"
	"testing"

	"github.com/mschulkind-oss/mavor/internal/audio"
)

// chunkOf builds one 30ms chunk of little-endian s16 whose samples are
// predictable, so a decode error shows up as a wrong number rather than as
// noise that still sounds plausible.
func chunkOf(start int) []byte {
	b := make([]byte, audio.FrameSamples*2)
	for i := range audio.FrameSamples {
		binary.LittleEndian.PutUint16(b[i*2:], uint16(int16(start+i)))
	}
	return b
}

func TestThePhraseBufferDecodesAndConcatenates(t *testing.T) {
	p := newPhraseBuffer()

	for tick := range 4 {
		frame := p.add(chunkOf(tick * 1000))
		if len(frame) != audio.FrameSamples {
			t.Fatalf("tick %d: add returned %d samples, want %d", tick, len(frame), audio.FrameSamples)
		}
		if frame[0] != int16(tick*1000) {
			t.Errorf("tick %d: frame starts at %d, want %d", tick, frame[0], tick*1000)
		}
	}

	phrase := p.take()
	if want := 4 * audio.FrameSamples; len(phrase) != want {
		t.Fatalf("phrase holds %d samples, want %d", len(phrase), want)
	}
	for tick := range 4 {
		if got, want := phrase[tick*audio.FrameSamples], int16(tick*1000); got != want {
			t.Errorf("phrase sample at tick %d is %d, want %d", tick, got, want)
		}
	}

	// take hands ownership away, so what follows must start empty and must
	// not write into the phrase the transcriber is now reading.
	next := p.add(chunkOf(9999))
	_ = next
	if len(p.samples) != audio.FrameSamples {
		t.Errorf("the buffer after take holds %d samples, want a fresh %d", len(p.samples), audio.FrameSamples)
	}
	if phrase[0] != 0 {
		t.Error("adding after take overwrote the phrase already handed to the transcriber")
	}
}

// The frame slice is reused, so a caller that retains it gets the next tick's
// audio. Said in a test because it is the kind of thing a future change here
// would break silently.
func TestThePhraseFrameIsOnlyValidUntilTheNextAdd(t *testing.T) {
	p := newPhraseBuffer()
	first := p.add(chunkOf(100))
	if first[0] != 100 {
		t.Fatalf("first frame starts at %d, want 100", first[0])
	}
	second := p.add(chunkOf(200))
	if &first[0] != &second[0] {
		t.Skip("the frame buffer was reallocated, so this run cannot show the aliasing")
	}
	if first[0] != 200 {
		t.Error("the frame slice was not reused; this is fine, but the documented contract now overstates the risk")
	}
}

// The reason for the type. This loop runs 33 times a second for as long as
// someone speaks without pausing, and it used to allocate a fresh frame every
// tick and grow the phrase by append — which, at append's ~1.25x growth
// factor for large slices, spends most of its time copying. Two minutes
// unbroken cost 27 MB across 4030 allocations to hold 3.84 MB of audio.
func TestThePhraseBufferDoesNotChurnWhileSomeoneKeepsTalking(t *testing.T) {
	const (
		ticks     = 4000 // two minutes at 30ms
		audioSize = ticks * audio.FrameSamples * 2
	)
	chunk := chunkOf(0)

	p := newPhraseBuffer()
	var before, after runtime.MemStats
	runtime.GC()
	runtime.ReadMemStats(&before)
	for range ticks {
		p.add(chunk)
	}
	runtime.ReadMemStats(&after)
	used := after.TotalAlloc - before.TotalAlloc

	t.Logf("%d ticks holding %d B of audio allocated %d B (%.1fx)",
		ticks, audioSize, used, float64(used)/float64(audioSize))

	// Doubling copies about the final size again on top of holding it; the
	// old shape cost seven times it. Three is comfortably between.
	if limit := uint64(audioSize) * 3; used > limit {
		t.Errorf("accumulating %d B of audio allocated %d B, over the %d B limit: "+
			"the buffer is churning per tick or growing by copying too often", audioSize, used, limit)
	}
}

// The same loop as a benchmark, at the three recording lengths the audio
// package measures its own paths at. It replaces a benchmark that lived in
// internal/audio and simulated this code by copying its shape — which stopped
// tracking it the moment the shape changed.
func BenchmarkPhraseAccumulation(b *testing.B) {
	chunk := chunkOf(0)
	for _, tc := range []struct {
		name  string
		ticks int
	}{
		{"5s_166ticks", 166},
		{"30s_1000ticks", 1000},
		{"120s_4000ticks", 4000},
	} {
		b.Run(tc.name, func(b *testing.B) {
			b.ReportAllocs()
			for b.Loop() {
				p := newPhraseBuffer()
				for range tc.ticks {
					p.add(chunk)
				}
				_ = p.take()
			}
		})
	}
}
