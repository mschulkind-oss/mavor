package audio

import (
	"path/filepath"
	"testing"
	"time"
)

func TestAudioDuration(t *testing.T) {
	fixture := filepath.Join("..", "..", "test", "fixtures", "real_speech.wav")
	dur, err := AudioDuration(fixture)
	if err != nil {
		t.Fatalf("AudioDuration failed: %v", err)
	}
	// real_speech.wav is exactly 20.00s
	if dur < 19*time.Second || dur > 21*time.Second {
		t.Fatalf("unexpected duration: %v (want ~20s)", dur)
	}
}

func TestSliceWAV(t *testing.T) {
	tmpDir := t.TempDir()
	srcWav := filepath.Join(tmpDir, "src.wav")

	// Generate 2 seconds of 16kHz mono audio (32,000 samples)
	samples := make([]int16, 32000)
	for i := range samples {
		samples[i] = int16(i % 1000)
	}
	if err := WriteWAV(srcWav, samples, DefaultSampleRate); err != nil {
		t.Fatalf("WriteWAV: %v", err)
	}

	dstWav := filepath.Join(tmpDir, "dst.wav")
	// Slice 0.5s to 1.5s (8,000 to 24,000 samples = 16,000 samples)
	if err := SliceWAV(srcWav, dstWav, 8000, 24000); err != nil {
		t.Fatalf("SliceWAV: %v", err)
	}

	dstSamples, err := ReadWAVSamples(dstWav)
	if err != nil {
		t.Fatalf("ReadWAVSamples: %v", err)
	}
	if len(dstSamples) != 16000 {
		t.Fatalf("expected 16000 samples, got %d", len(dstSamples))
	}
	for i := 0; i < 16000; i++ {
		if dstSamples[i] != samples[8000+i] {
			t.Fatalf("sample mismatch at %d: got %d, want %d", i, dstSamples[i], samples[8000+i])
		}
	}
}

func TestSliceWAV_InvalidRanges(t *testing.T) {
	tmpDir := t.TempDir()
	srcWav := filepath.Join(tmpDir, "src.wav")
	if err := WriteWAV(srcWav, make([]int16, 1600), DefaultSampleRate); err != nil {
		t.Fatal(err)
	}

	dstWav := filepath.Join(tmpDir, "dst.wav")
	if err := SliceWAV(srcWav, dstWav, 1000, 500); err == nil {
		t.Errorf("expected error for end <= start")
	}
	if err := SliceWAV("nonexistent.wav", dstWav, 0, 500); err == nil {
		t.Errorf("expected error for missing file")
	}
}

func TestFindSilenceSplitPoints(t *testing.T) {
	tmpDir := t.TempDir()
	srcWav := filepath.Join(tmpDir, "long_speech.wav")

	// Create 40s of audio:
	// 0s-18s loud speech (RMS ~0.05)
	// 18s-20s silence (RMS 0.0)
	// 20s-40s loud speech (RMS ~0.05)
	samples := make([]int16, 40*DefaultSampleRate)
	for i := range samples {
		sec := float64(i) / float64(DefaultSampleRate)
		if sec < 18.0 || sec > 20.0 {
			// loud signal
			samples[i] = 2000
		} else {
			// silence
			samples[i] = 0
		}
	}
	if err := WriteWAV(srcWav, samples, DefaultSampleRate); err != nil {
		t.Fatal(err)
	}

	splits, err := FindSilenceSplitPoints(srcWav, 15*time.Second, 25*time.Second, 0.012)
	if err != nil {
		t.Fatalf("FindSilenceSplitPoints failed: %v", err)
	}
	if len(splits) != 1 {
		t.Fatalf("expected 1 split point, got %d: %v", len(splits), splits)
	}
	// The split should be in the silence window [18s, 20s]
	if splits[0] < 18*time.Second || splits[0] > 20*time.Second {
		t.Fatalf("split point %v not in silence window [18s, 20s]", splits[0])
	}
}

func TestSilenceSlices(t *testing.T) {
	total := 40 * time.Second
	splits := []time.Duration{19 * time.Second}
	slices := SilenceSlices(total, splits)
	if len(slices) != 2 {
		t.Fatalf("expected 2 slices, got %d", len(slices))
	}
	if slices[0].StartTime != 0 || slices[0].EndTime != 19*time.Second {
		t.Errorf("slice 0 wrong time: %v..%v", slices[0].StartTime, slices[0].EndTime)
	}
	if slices[1].StartTime != 19*time.Second || slices[1].EndTime != 40*time.Second {
		t.Errorf("slice 1 wrong time: %v..%v", slices[1].StartTime, slices[1].EndTime)
	}
}

func TestOverlapSlices(t *testing.T) {
	total := 50 * time.Second
	window := 20 * time.Second
	overlap := 4 * time.Second // stride = 16s

	slices := OverlapSlices(total, window, overlap)
	// Expect:
	// slice 0: 0..20s
	// slice 1: 16..36s
	// slice 2: 32..50s
	if len(slices) != 3 {
		t.Fatalf("expected 3 slices, got %d: %+v", len(slices), slices)
	}
	if slices[0].StartTime != 0 || slices[0].EndTime != 20*time.Second {
		t.Errorf("slice 0: got %v..%v", slices[0].StartTime, slices[0].EndTime)
	}
	if slices[1].StartTime != 16*time.Second || slices[1].EndTime != 36*time.Second {
		t.Errorf("slice 1: got %v..%v", slices[1].StartTime, slices[1].EndTime)
	}
	if slices[2].StartTime != 32*time.Second || slices[2].EndTime != 50*time.Second {
		t.Errorf("slice 2: got %v..%v", slices[2].StartTime, slices[2].EndTime)
	}
}
