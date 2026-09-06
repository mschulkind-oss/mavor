package audio

// Benchmarks written to audit the audio hot path for cost that scales with
// something it shouldn't (recording duration), per-tick allocation, and
// redundant syscalls. Not part of the permanent suite — scratch benchmarks
// for a one-off performance audit. Safe to delete.
//
// Run with:
//   go test ./internal/audio/ -run XXX -bench . -benchmem

import (
	"encoding/binary"
	"fmt"
	"os"
	"path/filepath"
	"testing"
)

// makeWAVFile writes a realistic parec-style WAV (RIFF/fmt/data, no LIST
// chunk — matches DefaultCommand's --file-format=wav output) with the given
// duration at 16kHz mono s16le, and returns its path.
func makeWAVFile(tb testing.TB, seconds int) string {
	tb.Helper()
	dir := tb.TempDir()
	path := filepath.Join(dir, fmt.Sprintf("rec-%ds.wav", seconds))

	sampleRate := 16000
	numSamples := sampleRate * seconds
	dataSize := numSamples * 2

	f, err := os.Create(path)
	if err != nil {
		tb.Fatal(err)
	}
	defer f.Close()

	header := make([]byte, 44)
	copy(header[0:4], "RIFF")
	binary.LittleEndian.PutUint32(header[4:8], uint32(36+dataSize))
	copy(header[8:12], "WAVE")
	copy(header[12:16], "fmt ")
	binary.LittleEndian.PutUint32(header[16:20], 16)
	binary.LittleEndian.PutUint16(header[20:22], 1)
	binary.LittleEndian.PutUint16(header[22:24], 1)
	binary.LittleEndian.PutUint32(header[24:28], uint32(sampleRate))
	binary.LittleEndian.PutUint32(header[28:32], uint32(sampleRate*2))
	binary.LittleEndian.PutUint16(header[32:34], 2)
	binary.LittleEndian.PutUint16(header[34:36], 16)
	copy(header[36:40], "data")
	binary.LittleEndian.PutUint32(header[40:44], uint32(dataSize))
	if _, err := f.Write(header); err != nil {
		tb.Fatal(err)
	}

	// Write a simple tone pattern so RMS/VAD math has real work to do,
	// in 64KB chunks so we don't need a 3.8MB buffer for the 120s case.
	buf := make([]byte, 1<<16)
	written := 0
	sample := int16(0)
	for written < dataSize {
		n := len(buf)
		if dataSize-written < n {
			n = dataSize - written
		}
		for i := 0; i+1 < n; i += 2 {
			sample += 137 // cheap pseudo-waveform, non-zero so RMS isn't trivially 0
			binary.LittleEndian.PutUint16(buf[i:i+2], uint16(sample))
		}
		if _, err := f.Write(buf[:n]); err != nil {
			tb.Fatal(err)
		}
		written += n
	}
	return path
}

var recordingDurations = []int{5, 30, 120}

// BenchmarkReadWAVSamples proves ReadWAVSamples (called once, synchronously,
// by DetectSpeech in the transcription hot path — see daemon.go's
// runTranscription) reads and converts the ENTIRE file, so its cost is
// linear in total recording length.
func BenchmarkReadWAVSamples(b *testing.B) {
	for _, secs := range recordingDurations {
		path := makeWAVFile(b, secs)
		b.Run(fmt.Sprintf("%ds", secs), func(b *testing.B) {
			b.ReportAllocs()
			for i := 0; i < b.N; i++ {
				samples, err := ReadWAVSamples(path)
				if err != nil {
					b.Fatal(err)
				}
				if len(samples) == 0 {
					b.Fatal("expected samples")
				}
			}
		})
	}
}

// BenchmarkDetectSpeech is the actual call site used in daemon.go's
// runTranscription VAD pre-filter — one call per completed recording.
func BenchmarkDetectSpeech(b *testing.B) {
	for _, secs := range recordingDurations {
		path := makeWAVFile(b, secs)
		b.Run(fmt.Sprintf("%ds", secs), func(b *testing.B) {
			b.ReportAllocs()
			for i := 0; i < b.N; i++ {
				if _, err := DetectSpeech(path, 0); err != nil {
					b.Fatal(err)
				}
			}
		})
	}
}

// BenchmarkReadRecentSamples simulates one tick of ParecRecorder.monitorLevel
// (recorder.go:108-123), which calls this every 30ms while recording is in
// progress. If cost were proportional to file size, a long recording would
// make the level meter itself expensive. This benchmark shows whether it is.
func BenchmarkReadRecentSamples(b *testing.B) {
	for _, secs := range recordingDurations {
		path := makeWAVFile(b, secs)
		b.Run(fmt.Sprintf("%ds", secs), func(b *testing.B) {
			b.ReportAllocs()
			for i := 0; i < b.N; i++ {
				samples, err := ReadRecentSamples(path, FrameSamples)
				if err != nil {
					b.Fatal(err)
				}
				if len(samples) == 0 {
					b.Fatal("expected samples")
				}
			}
		})
	}
}

// BenchmarkWAVDataOffset isolates the header-walk cost that both ReadChunk
// (recorder.go:193) and ReadRecentSamples/ReadWAVSamples redo on every call,
// even though the data offset of a given file never changes once parec has
// written its header. Shows whether that's a meaningful redundant cost.
func BenchmarkWAVDataOffset(b *testing.B) {
	for _, secs := range recordingDurations {
		path := makeWAVFile(b, secs)
		b.Run(fmt.Sprintf("%ds", secs), func(b *testing.B) {
			f, err := os.Open(path)
			if err != nil {
				b.Fatal(err)
			}
			defer f.Close()
			b.ReportAllocs()
			for i := 0; i < b.N; i++ {
				if _, err := WAVDataOffset(f); err != nil {
					b.Fatal(err)
				}
			}
		})
	}
}

// BenchmarkReadChunkSteadyState simulates ParecRecorder.ReadChunk being
// polled every 30ms by runStreamPreview/runPhrasePreview (daemon.go:383,453)
// against a file that has already grown to `secs` seconds, reading only the
// most recent ~30ms (960 bytes) each call — the steady-state case once a
// long recording is already in progress. Cost here should be flat (O(1) in
// how much has already been recorded), unlike ReadWAVSamples above.
func BenchmarkReadChunkSteadyState(b *testing.B) {
	for _, secs := range recordingDurations {
		path := makeWAVFile(b, secs)
		fi, err := os.Stat(path)
		if err != nil {
			b.Fatal(err)
		}
		// readOffset positioned 960 bytes (30ms @ 16kHz s16le) before EOF,
		// mimicking a recorder that just wrote one more tick's worth.
		offset := fi.Size() - 960
		b.Run(fmt.Sprintf("%ds", secs), func(b *testing.B) {
			b.ReportAllocs()
			r := &ParecRecorder{outPath: path, readOffset: offset}
			for i := 0; i < b.N; i++ {
				r.readOffset = offset // reset each iteration to re-read the same tail
				chunk, err := r.ReadChunk()
				if err != nil {
					b.Fatal(err)
				}
				if len(chunk) == 0 {
					b.Fatal("expected chunk")
				}
			}
		})
	}
}

// BenchmarkPhrasePreviewAccumulation reproduces the per-tick allocation and
// append pattern in daemon.go's runPhrasePreview (around line 461-471):
// every 30ms tick allocates a fresh []int16 for the chunk, then appends it
// onto a slice that keeps growing until a silence gap resets it. This
// benchmarks the cost of N ticks' worth of that pattern for a phrase as long
// as a whole recording (i.e. continuous speech with no pause).
func BenchmarkPhrasePreviewAccumulation(b *testing.B) {
	chunkBytes := 960 // 30ms @ 16kHz s16le
	chunk := make([]byte, chunkBytes)
	for i := range chunk {
		chunk[i] = byte(i)
	}
	for _, secs := range recordingDurations {
		ticks := secs * 1000 / 30
		b.Run(fmt.Sprintf("%ds_%dticks", secs, ticks), func(b *testing.B) {
			b.ReportAllocs()
			for i := 0; i < b.N; i++ {
				var accumulated []int16
				for t := 0; t < ticks; t++ {
					sampleCount := len(chunk) / 2
					frameSamples := make([]int16, sampleCount) // per-tick alloc, as in daemon.go
					for j := 0; j < sampleCount; j++ {
						frameSamples[j] = int16(binary.LittleEndian.Uint16(chunk[j*2 : j*2+2]))
					}
					accumulated = append(accumulated, frameSamples...)
				}
				if len(accumulated) == 0 {
					b.Fatal("expected accumulation")
				}
			}
		})
	}
}
