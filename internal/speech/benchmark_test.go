package speech

import (
	"context"
	"encoding/binary"
	"fmt"
	"math"
	"os"
	"os/exec"
	"path/filepath"
	"testing"
	"time"

	"github.com/mschulkind-oss/mavor/internal/audio"
)

// createBenchmarkWAV writes a 16kHz 16-bit mono PCM WAV file of the specified duration.
func createBenchmarkWAV(path string, durationSec float64, freq float64) error {
	sampleRate := audio.DefaultSampleRate
	numSamples := int(float64(sampleRate) * durationSec)
	dataSize := uint32(numSamples * 2)
	fileSize := 36 + dataSize

	f, err := os.Create(path)
	if err != nil {
		return err
	}
	defer f.Close()

	header := make([]byte, 44)
	copy(header[0:4], "RIFF")
	binary.LittleEndian.PutUint32(header[4:8], fileSize)
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
	binary.LittleEndian.PutUint32(header[40:44], dataSize)

	if _, err := f.Write(header); err != nil {
		return err
	}

	pcm := make([]byte, dataSize)
	for i := 0; i < numSamples; i++ {
		t := float64(i) / float64(sampleRate)
		val := 0.3 * math.Sin(2*math.Pi*freq*t)
		sample := int16(val * 32767.0)
		binary.LittleEndian.PutUint16(pcm[i*2:i*2+2], uint16(sample))
	}
	_, err = f.Write(pcm)
	return err
}

// generateSineSamples generates in-memory audio samples at 16kHz.
func generateSineSamples(durationSec float64, freq float64) []int16 {
	numSamples := int(float64(audio.DefaultSampleRate) * durationSec)
	samples := make([]int16, numSamples)
	for i := 0; i < numSamples; i++ {
		t := float64(i) / float64(audio.DefaultSampleRate)
		val := 0.3 * math.Sin(2*math.Pi*freq*t)
		samples[i] = int16(val * 32767.0)
	}
	return samples
}

// findBenchmarkModel looks for an available GGML model file for transcription benchmarks.
func findBenchmarkModel() string {
	home, _ := os.UserHomeDir()
	candidates := []string{
		os.Getenv("MAVOR_MODEL_PATH"),
		filepath.Join(home, ".cache/mavor/models/ggml-base.en.bin"),
		filepath.Join(home, ".cache/mavor/models/ggml-tiny.en.bin"),
	}
	for _, c := range candidates {
		if c != "" {
			if _, err := os.Stat(c); err == nil {
				return c
			}
		}
	}
	return ""
}

// BenchmarkWhisperCli_Mock measures the Go wrapper and process orchestration overhead
// using a simulated whisper command.
func BenchmarkWhisperCli_Mock(b *testing.B) {
	tmpDir := b.TempDir()
	wavPath := filepath.Join(tmpDir, "mock.wav")
	if err := createBenchmarkWAV(wavPath, 1.0, 440.0); err != nil {
		b.Fatal(err)
	}

	w := NewWhisperCli("mock-model")
	w.Build = func(ctx context.Context, _, targetWav string) *exec.Cmd {
		return exec.CommandContext(ctx, "sh", "-c",
			`printf "benchmark transcription text\n" > "$1.txt"`, "sh", targetWav)
	}

	ctx := context.Background()
	b.ResetTimer()
	b.ReportAllocs()

	for i := 0; i < b.N; i++ {
		text, err := w.Transcribe(ctx, wavPath)
		if err != nil {
			b.Fatalf("Transcribe failed: %v", err)
		}
		if text == "" {
			b.Fatal("Transcribe returned empty text")
		}
	}
}

// BenchmarkWhisperCli_Transcribe measures real whisper-cli execution across audio durations.
func BenchmarkWhisperCli_Transcribe(b *testing.B) {
	whisperBin, err := exec.LookPath("whisper-cli")
	if err != nil {
		b.Skip("whisper-cli binary not found in PATH")
	}
	modelPath := findBenchmarkModel()
	if modelPath == "" {
		b.Skip("no whisper GGML model found in cache or MAVOR_MODEL_PATH")
	}

	durations := []struct {
		name string
		sec  float64
	}{
		{"1s", 1.0},
		{"5s", 5.0},
		{"15s", 15.0},
	}

	tmpDir := b.TempDir()
	ctx := context.Background()

	for _, d := range durations {
		b.Run(d.name, func(b *testing.B) {
			wavPath := filepath.Join(tmpDir, fmt.Sprintf("bench_%s.wav", d.name))
			if err := createBenchmarkWAV(wavPath, d.sec, 440.0); err != nil {
				b.Fatalf("failed to create fixture: %v", err)
			}

			w := NewWhisperCli(modelPath)
			// Use whisper-cli with threads matching hardware concurrency
			w.Threads = 4

			b.ResetTimer()
			b.ReportAllocs()

			for i := 0; i < b.N; i++ {
				_, err := w.Transcribe(ctx, wavPath)
				if err != nil {
					b.Fatalf("real Transcribe failed (bin=%s model=%s): %v", whisperBin, modelPath, err)
				}
			}
		})
	}
}

// BenchmarkVAD_CalculateRMS benchmarks the RMS energy computation for standard 30ms frames (480 samples).
func BenchmarkVAD_CalculateRMS(b *testing.B) {
	frame := generateSineSamples(0.03, 440.0) // 30ms = 480 samples
	if len(frame) != 480 {
		frame = make([]int16, 480)
	}

	b.SetBytes(int64(len(frame) * 2))
	b.ResetTimer()
	b.ReportAllocs()

	var totalRMS float64
	for i := 0; i < b.N; i++ {
		totalRMS += audio.CalculateRMS(frame)
	}
	if totalRMS == 0 && b.N > 0 {
		b.Log("totalRMS is zero")
	}
}

// BenchmarkVAD_SpeechDuration benchmarks speech duration calculation over raw sample buffers.
func BenchmarkVAD_SpeechDuration(b *testing.B) {
	durations := []struct {
		name string
		sec  float64
	}{
		{"1s", 1.0},
		{"5s", 5.0},
		{"15s", 15.0},
	}

	for _, d := range durations {
		b.Run(d.name, func(b *testing.B) {
			samples := generateSineSamples(d.sec, 440.0)
			b.SetBytes(int64(len(samples) * 2))
			b.ResetTimer()
			b.ReportAllocs()

			var totalDuration time.Duration
			for i := 0; i < b.N; i++ {
				totalDuration += audio.SpeechDuration(samples, audio.DefaultSampleRate, audio.SpeechRMSThreshold)
			}
			if totalDuration == 0 && b.N > 0 {
				b.Log("totalDuration is zero")
			}
		})
	}
}

// BenchmarkVAD_DetectSpeech benchmarks full VAD pipeline including WAV reading and parsing.
func BenchmarkVAD_DetectSpeech(b *testing.B) {
	tmpDir := b.TempDir()
	durations := []struct {
		name string
		sec  float64
	}{
		{"1s", 1.0},
		{"5s", 5.0},
		{"15s", 15.0},
	}

	for _, d := range durations {
		b.Run(d.name, func(b *testing.B) {
			wavPath := filepath.Join(tmpDir, fmt.Sprintf("vad_%s.wav", d.name))
			if err := createBenchmarkWAV(wavPath, d.sec, 440.0); err != nil {
				b.Fatalf("failed to create fixture: %v", err)
			}

			fi, err := os.Stat(wavPath)
			if err != nil {
				b.Fatal(err)
			}
			b.SetBytes(fi.Size())
			b.ResetTimer()
			b.ReportAllocs()

			for i := 0; i < b.N; i++ {
				hasSpeech, err := audio.DetectSpeech(wavPath, 200*time.Millisecond)
				if err != nil {
					b.Fatalf("DetectSpeech failed: %v", err)
				}
				if !hasSpeech {
					b.Fatal("expected speech to be detected")
				}
			}
		})
	}
}
