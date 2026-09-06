package daemon

import (
	"encoding/binary"

	"github.com/mschulkind-oss/mavor/internal/audio"
)

// Phrase-buffer sizes, in samples at DefaultSampleRate.
const (
	// initialPhraseCapacity is eight seconds: longer than most phrases, and
	// 256 KB, which is not worth being careful about.
	initialPhraseCapacity = audio.DefaultSampleRate * 8

	// maxPhraseCapacity caps what one long phrase makes the next buffer
	// carry, so a single unbroken minute does not hold a minute's worth of
	// array for the rest of the session.
	maxPhraseCapacity = audio.DefaultSampleRate * 30
)

// phraseBuffer accumulates the samples of one phrase for the phrase-mode
// preview, which appends a 30 ms chunk 33 times a second for as long as the
// user speaks without pausing.
//
// It is a type rather than three variables in the loop so that its allocation
// behaviour can be tested. The loop itself needs a daemon, a recorder and a
// wall clock; this needs a byte slice.
type phraseBuffer struct {
	samples []int16
	frame   []int16
}

func newPhraseBuffer() *phraseBuffer {
	return &phraseBuffer{
		samples: make([]int16, 0, initialPhraseCapacity),
		frame:   make([]int16, 0, audio.FrameSamples),
	}
}

// add decodes one little-endian s16 chunk onto the phrase and returns just
// that chunk's samples, for the caller's RMS check. The returned slice is
// reused by the next call and must not be retained.
func (p *phraseBuffer) add(chunk []byte) []int16 {
	n := len(chunk) / 2
	if cap(p.frame) < n {
		p.frame = make([]int16, n)
	}
	p.frame = p.frame[:n]
	for i := range n {
		p.frame[i] = int16(binary.LittleEndian.Uint16(chunk[i*2 : i*2+2]))
	}

	// Grow by doubling rather than letting append do it.
	//
	// append's growth factor tends towards 1.25 for large slices, and the
	// copies that costs dominate everything else here: reaching two minutes
	// of audio that way copies roughly five times the final size. Doubling
	// costs two.
	if len(p.samples)+n > cap(p.samples) {
		grown := make([]int16, len(p.samples), max(2*cap(p.samples), len(p.samples)+n))
		copy(grown, p.samples)
		p.samples = grown
	}
	p.samples = append(p.samples, p.frame...)
	return p.frame
}

// take hands the phrase to a caller that will own it — transcribePhrase runs
// on its own goroutine — and starts a fresh buffer. The new one is sized from
// what the last phrase actually needed, so a steady stream of similar phrases
// stops growing after the first.
func (p *phraseBuffer) take() []int16 {
	phrase := p.samples
	p.samples = make([]int16, 0, min(max(cap(phrase), initialPhraseCapacity), maxPhraseCapacity))
	return phrase
}
