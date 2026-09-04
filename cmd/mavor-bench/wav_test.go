package main

import (
	"encoding/binary"
	"math"
	"os"
	"path/filepath"
	"testing"
)

// writeWAV builds a minimal 16 kHz mono 16-bit PCM file of the given length.
func writeWAV(t *testing.T, seconds float64) string {
	t.Helper()
	const sampleRate, channels, bits = 16000, 1, 16
	byteRate := sampleRate * channels * bits / 8
	dataLen := int(seconds * float64(byteRate))

	buf := make([]byte, 0, 44+dataLen)
	put32 := func(v uint32) { b := make([]byte, 4); binary.LittleEndian.PutUint32(b, v); buf = append(buf, b...) }
	put16 := func(v uint16) { b := make([]byte, 2); binary.LittleEndian.PutUint16(b, v); buf = append(buf, b...) }

	buf = append(buf, "RIFF"...)
	put32(uint32(36 + dataLen))
	buf = append(buf, "WAVE"...)
	buf = append(buf, "fmt "...)
	put32(16)
	put16(1)
	put16(channels)
	put32(sampleRate)
	put32(uint32(byteRate))
	put16(channels * bits / 8)
	put16(bits)
	buf = append(buf, "data"...)
	put32(uint32(dataLen))
	buf = append(buf, make([]byte, dataLen)...)

	path := filepath.Join(t.TempDir(), "test.wav")
	if err := os.WriteFile(path, buf, 0o644); err != nil {
		t.Fatal(err)
	}
	return path
}

func TestWavDurationSeconds(t *testing.T) {
	// Every RTF in the report divides by this, so an error here scales the
	// whole report by a constant and still looks entirely plausible.
	for _, want := range []float64{1, 5, 20} {
		got, err := wavDurationSeconds(writeWAV(t, want))
		if err != nil {
			t.Fatalf("wavDurationSeconds: %v", err)
		}
		if math.Abs(got-want) > 0.001 {
			t.Errorf("duration = %v s, want %v s", got, want)
		}
	}
}

func TestWavDurationRejectsNonWAV(t *testing.T) {
	path := filepath.Join(t.TempDir(), "not.wav")
	if err := os.WriteFile(path, []byte("this is not a wav file at all"), 0o644); err != nil {
		t.Fatal(err)
	}
	if _, err := wavDurationSeconds(path); err == nil {
		t.Error("wavDurationSeconds accepted a file that is not RIFF/WAVE")
	}
}

func TestWavDurationMatchesTheRealFixture(t *testing.T) {
	// The fixture the benchmark actually runs against. Guards against a
	// silent re-record changing every published RTF.
	got, err := wavDurationSeconds("../../test/fixtures/real_speech.wav")
	if err != nil {
		t.Skipf("fixture unavailable: %v", err)
	}
	if got < 15 || got > 25 {
		t.Errorf("real_speech.wav is %.2f s; the report's numbers assume about 20 s", got)
	}
}

func TestPCM16LERoundTripsThroughTheSampleRange(t *testing.T) {
	// FeedChunk takes the same little-endian s16 the recorder produces, so a
	// conversion bug here would feed the streaming models noise and show up
	// as a mysteriously bad WER on streaming rows only.
	// Full scale maps symmetrically to +/-32767, so -1.0 is 0x8001 and not
	// 0x8000: the extra negative code is reserved for genuinely clipped
	// input, which the clamp test below covers.
	got := pcm16LE([]float32{0, 1, -1})
	want := []byte{0x00, 0x00, 0xFF, 0x7F, 0x01, 0x80}
	if len(got) != len(want) {
		t.Fatalf("pcm16LE returned %d bytes, want %d", len(got), len(want))
	}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("pcm16LE = % x, want % x", got, want)
		}
	}
}

func TestPCM16LEClampsOutOfRangeSamples(t *testing.T) {
	// Clipped audio must saturate, not wrap: a wrapped sample flips a loud
	// positive peak to a loud negative one and sounds like a click.
	got := pcm16LE([]float32{2.5, -2.5})
	if got[0] != 0xFF || got[1] != 0x7F {
		t.Errorf("a sample above 1.0 gave % x, want saturation to 0x7FFF", got[0:2])
	}
	if got[2] != 0x00 || got[3] != 0x80 {
		t.Errorf("a sample below -1.0 gave % x, want saturation to 0x8000", got[2:4])
	}
}
