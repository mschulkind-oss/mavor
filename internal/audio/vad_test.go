package audio

import (
	"encoding/binary"
	"math"
	"os"
	"path/filepath"
	"testing"
	"time"
)

func writeTestWAV(t *testing.T, path string, samples []int16) {
	t.Helper()
	f, err := os.Create(path)
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()

	dataSize := uint32(len(samples) * 2)
	fileSize := 36 + dataSize

	// Write 44-byte standard RIFF/WAVE header
	header := make([]byte, 44)
	copy(header[0:4], "RIFF")
	binary.LittleEndian.PutUint32(header[4:8], fileSize)
	copy(header[8:12], "WAVE")
	copy(header[12:16], "fmt ")
	binary.LittleEndian.PutUint32(header[16:20], 16)    // PCM chunk size
	binary.LittleEndian.PutUint16(header[20:22], 1)     // Audio format 1 = PCM
	binary.LittleEndian.PutUint16(header[22:24], 1)     // Num channels = 1
	binary.LittleEndian.PutUint32(header[24:28], 16000) // Sample rate = 16000
	binary.LittleEndian.PutUint32(header[28:32], 32000) // Byte rate = 16000 * 2
	binary.LittleEndian.PutUint16(header[32:34], 2)     // Block align = 2
	binary.LittleEndian.PutUint16(header[34:36], 16)    // Bits per sample = 16
	copy(header[36:40], "data")
	binary.LittleEndian.PutUint32(header[40:44], dataSize)

	if _, err := f.Write(header); err != nil {
		t.Fatal(err)
	}

	raw := make([]byte, len(samples)*2)
	for i, s := range samples {
		binary.LittleEndian.PutUint16(raw[i*2:i*2+2], uint16(s))
	}
	if _, err := f.Write(raw); err != nil {
		t.Fatal(err)
	}
}

func generateSineTone(sampleRate int, freq float64, duration time.Duration, amplitude float64) []int16 {
	numSamples := int(float64(sampleRate) * duration.Seconds())
	samples := make([]int16, numSamples)
	for i := 0; i < numSamples; i++ {
		t := float64(i) / float64(sampleRate)
		val := math.Sin(2 * math.Pi * freq * t)
		samples[i] = int16(val * amplitude * 32767.0)
	}
	return samples
}

func TestDetectSpeech(t *testing.T) {
	dir := t.TempDir()

	// 1. Silent file (0 amplitude for 1 second)
	silentPath := filepath.Join(dir, "silent.wav")
	silentSamples := make([]int16, 16000)
	writeTestWAV(t, silentPath, silentSamples)

	hasSpeech, err := DetectSpeech(silentPath, 200*time.Millisecond)
	if err != nil {
		t.Fatalf("DetectSpeech(silent) err: %v", err)
	}
	if hasSpeech {
		t.Errorf("DetectSpeech(silent) = true, want false")
	}

	// 2. Active voice tone (440Hz sine wave for 500ms at 50% amplitude)
	tonePath := filepath.Join(dir, "tone.wav")
	toneSamples := generateSineTone(16000, 440.0, 500*time.Millisecond, 0.5)
	writeTestWAV(t, tonePath, toneSamples)

	hasSpeech, err = DetectSpeech(tonePath, 200*time.Millisecond)
	if err != nil {
		t.Fatalf("DetectSpeech(tone) err: %v", err)
	}
	if !hasSpeech {
		t.Errorf("DetectSpeech(tone) = false, want true")
	}

	// 3. Very brief noise pulse (50ms), should fail 200ms threshold
	pulsePath := filepath.Join(dir, "pulse.wav")
	pulseSamples := generateSineTone(16000, 440.0, 50*time.Millisecond, 0.5)
	writeTestWAV(t, pulsePath, pulseSamples)

	hasSpeech, err = DetectSpeech(pulsePath, 200*time.Millisecond)
	if err != nil {
		t.Fatalf("DetectSpeech(pulse) err: %v", err)
	}
	if hasSpeech {
		t.Errorf("DetectSpeech(pulse 50ms) = true, want false for 200ms threshold")
	}
}

func TestCalculateRMS(t *testing.T) {
	// Silence has RMS 0
	zero := make([]int16, 480)
	if rms := CalculateRMS(zero); rms != 0.0 {
		t.Errorf("CalculateRMS(zero) = %v, want 0.0", rms)
	}

	// Full scale square wave has RMS 1.0
	full := make([]int16, 480)
	for i := range full {
		full[i] = 32767
	}
	if rms := CalculateRMS(full); math.Abs(rms-1.0) > 0.01 {
		t.Errorf("CalculateRMS(full) = %v, want ~1.0", rms)
	}
}

func TestReadRecentSamples(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "recent.wav")

	// 1. File does not exist
	if _, err := ReadRecentSamples(filepath.Join(dir, "nonexistent.wav"), 480); err == nil {
		t.Errorf("expected error for non-existent file")
	}

	// 2. Empty WAV / header only
	f, _ := os.Create(path)
	_ = f.Close()
	samples, err := ReadRecentSamples(path, 480)
	if err != nil || samples != nil {
		t.Errorf("ReadRecentSamples on 0-byte file = %v, err=%v", samples, err)
	}

	// 3. Normal WAV with 1000 samples
	origSamples := make([]int16, 1000)
	for i := range origSamples {
		origSamples[i] = int16(i)
	}
	writeTestWAV(t, path, origSamples)

	// Read last 480 samples
	got, err := ReadRecentSamples(path, 480)
	if err != nil {
		t.Fatalf("ReadRecentSamples err: %v", err)
	}
	if len(got) != 480 {
		t.Fatalf("len(got) = %d, want 480", len(got))
	}
	for i := 0; i < 480; i++ {
		want := origSamples[1000-480+i]
		if got[i] != want {
			t.Fatalf("got[%d] = %d, want %d", i, got[i], want)
		}
	}

	// Read more samples than file contains (request 2000, file has 1000)
	gotAll, err := ReadRecentSamples(path, 2000)
	if err != nil {
		t.Fatalf("ReadRecentSamples err: %v", err)
	}
	if len(gotAll) != 1000 {
		t.Fatalf("len(gotAll) = %d, want 1000", len(gotAll))
	}
}
