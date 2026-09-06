package speech

// Benchmark for the per-tick conversion in cgoOnlineRecognizer.FeedChunk
// (sherpa_cgo.go:146-165), which runs on the goroutine daemon.go's
// runStreamPreview drives at one tick per 30ms while the companion (or a
// streaming main model) is live:
//
//	numSamples := len(chunk) / 2
//	samples := make([]float32, numSamples)
//	for i := 0; i < numSamples; i++ {
//	    val := int16(binary.LittleEndian.Uint16(chunk[i*2 : i*2+2]))
//	    samples[i] = float32(val) / 32768.0
//	}
//
// This isolates that conversion (pure Go, no cgo/model needed) to measure
// its allocation and time cost per chunk, and projects it across a whole
// dictation's worth of ticks. Scratch benchmark for a one-off performance
// audit; safe to delete.
//
// Run with:
//
//	go test ./internal/speech/ -run XXX -bench FeedChunkConversion -benchmem

import (
	"encoding/binary"
	"fmt"
	"testing"
)

// int16ToFloat32Alloc is the exact conversion FeedChunk performs, allocating
// a fresh slice every call — as the real code at sherpa_cgo.go:154-159 does.
func int16ToFloat32Alloc(chunk []byte) []float32 {
	numSamples := len(chunk) / 2
	samples := make([]float32, numSamples)
	for i := 0; i < numSamples; i++ {
		val := int16(binary.LittleEndian.Uint16(chunk[i*2 : i*2+2]))
		samples[i] = float32(val) / 32768.0
	}
	return samples
}

// int16ToFloat32Reuse is the same conversion writing into a caller-owned
// buffer, to quantify what a pooled/reused buffer would save.
func int16ToFloat32Reuse(chunk []byte, dst []float32) []float32 {
	numSamples := len(chunk) / 2
	if cap(dst) < numSamples {
		dst = make([]float32, numSamples)
	}
	dst = dst[:numSamples]
	for i := 0; i < numSamples; i++ {
		val := int16(binary.LittleEndian.Uint16(chunk[i*2 : i*2+2]))
		dst[i] = float32(val) / 32768.0
	}
	return dst
}

// BenchmarkFeedChunkConversion_PerCallAlloc reproduces the live production
// call pattern: a fresh chunk-sized slice allocated on every tick.
func BenchmarkFeedChunkConversion_PerCallAlloc(b *testing.B) {
	for _, ms := range []int{30, 100} {
		samples := (16000 * ms) / 1000
		chunk := make([]byte, samples*2)
		for i := range chunk {
			chunk[i] = byte(i)
		}
		b.Run(fmt.Sprintf("%dms_chunk", ms), func(b *testing.B) {
			b.ReportAllocs()
			for i := 0; i < b.N; i++ {
				out := int16ToFloat32Alloc(chunk)
				if len(out) == 0 {
					b.Fatal("expected samples")
				}
			}
			// Ticks per second is 1000/ms; report the steady-state per-second
			// cost this pattern adds while a stream is live.
			ticksPerSec := 1000 / ms
			b.ReportMetric(float64(ticksPerSec)*float64(b.Elapsed())/float64(b.N)/1e6, "ms-equivalent/sec-of-audio")
		})
	}
}

// BenchmarkFeedChunkConversion_ReusedBuffer is the same conversion against a
// buffer reused across calls, showing what a pool would save.
func BenchmarkFeedChunkConversion_ReusedBuffer(b *testing.B) {
	for _, ms := range []int{30, 100} {
		samples := (16000 * ms) / 1000
		chunk := make([]byte, samples*2)
		for i := range chunk {
			chunk[i] = byte(i)
		}
		b.Run(fmt.Sprintf("%dms_chunk", ms), func(b *testing.B) {
			b.ReportAllocs()
			var buf []float32
			for i := 0; i < b.N; i++ {
				buf = int16ToFloat32Reuse(chunk, buf)
				if len(buf) == 0 {
					b.Fatal("expected samples")
				}
			}
		})
	}
}
