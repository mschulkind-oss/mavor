package daemon

import (
	"encoding/binary"
	"testing"
)

// BenchmarkPhrasePreviewByteToInt16 measures the byte->int16 conversion
// runPhrasePreview does on every 30ms tick (daemon.go's ticker in
// runPhrasePreview): a fresh []int16 allocation plus a manual
// binary.LittleEndian.Uint16 loop, then an append onto the accumulator. This
// only runs in the phrase-mode preview fallback (no streaming-capable model
// and no companion loaded), and only while actively recording, so its
// absolute cost is small — this benchmark exists to put a number on it rather
// than leave it unmeasured.
//
// chunkBytes = 960 is 30ms of 16kHz mono 16-bit PCM (audio.DefaultSampleRate),
// which is the real chunk size ReadChunk hands back on the recorder's 30ms
// cadence.
func BenchmarkPhrasePreviewByteToInt16(b *testing.B) {
	const chunkBytes = 960
	chunk := make([]byte, chunkBytes)
	for i := range chunk {
		chunk[i] = byte(i)
	}
	var accumulated []int16

	b.ReportAllocs()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		sampleCount := len(chunk) / 2
		frameSamples := make([]int16, sampleCount)
		for j := 0; j < sampleCount; j++ {
			frameSamples[j] = int16(binary.LittleEndian.Uint16(chunk[j*2 : j*2+2]))
		}
		accumulated = append(accumulated, frameSamples...)
		// Reset periodically so the accumulator doesn't grow without bound
		// and start dominating the measurement — a real recording resets it
		// at every detected phrase boundary.
		if len(accumulated) > 16000*3 {
			accumulated = accumulated[:0]
		}
	}
}
