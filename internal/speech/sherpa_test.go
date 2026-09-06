package speech

import (
	"bytes"
	"context"
	"encoding/binary"
	"errors"
	"log/slog"
	"math"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/mschulkind-oss/mavor/internal/config"
)

// helper to create a valid dummy WAV file
func createTestWAV(t *testing.T, path string, sampleRate int, numChannels int, bitsPerSample int, audioFormat int, numFrames int) {
	t.Helper()
	var buf bytes.Buffer

	// Write RIFF header placeholder
	buf.WriteString("RIFF")
	binary.Write(&buf, binary.LittleEndian, uint32(0)) // total size placeholder
	buf.WriteString("WAVE")

	// Write fmt chunk
	buf.WriteString("fmt ")
	binary.Write(&buf, binary.LittleEndian, uint32(16)) // fmt size
	binary.Write(&buf, binary.LittleEndian, uint16(audioFormat))
	binary.Write(&buf, binary.LittleEndian, uint16(numChannels))
	binary.Write(&buf, binary.LittleEndian, uint32(sampleRate))
	byteRate := uint32(sampleRate * numChannels * (bitsPerSample / 8))
	blockAlign := uint16(numChannels * (bitsPerSample / 8))
	binary.Write(&buf, binary.LittleEndian, byteRate)
	binary.Write(&buf, binary.LittleEndian, blockAlign)
	binary.Write(&buf, binary.LittleEndian, uint16(bitsPerSample))

	// Write data chunk
	buf.WriteString("data")
	dataSize := uint32(numFrames * int(blockAlign))
	binary.Write(&buf, binary.LittleEndian, dataSize)

	for i := 0; i < numFrames; i++ {
		// Generate simple tone/ramp
		valFloat := float32(math.Sin(float64(i) * 0.1))
		for ch := 0; ch < numChannels; ch++ {
			switch {
			case audioFormat == 1 && bitsPerSample == 16:
				valInt16 := int16(valFloat * 32767.0)
				binary.Write(&buf, binary.LittleEndian, valInt16)
			case audioFormat == 1 && bitsPerSample == 24:
				valInt32 := int32(valFloat * 8388607.0)
				buf.WriteByte(byte(valInt32))
				buf.WriteByte(byte(valInt32 >> 8))
				buf.WriteByte(byte(valInt32 >> 16))
			case audioFormat == 1 && bitsPerSample == 32:
				valInt32 := int32(valFloat * 2147483647.0)
				binary.Write(&buf, binary.LittleEndian, valInt32)
			case audioFormat == 3 && bitsPerSample == 32:
				binary.Write(&buf, binary.LittleEndian, valFloat)
			}
		}
	}

	data := buf.Bytes()
	totalSize := uint32(len(data) - 8)
	binary.LittleEndian.PutUint32(data[4:8], totalSize)

	if err := os.WriteFile(path, data, 0o644); err != nil {
		t.Fatalf("failed to write test wav: %v", err)
	}
}

func TestReadWAVAudioFormats(t *testing.T) {
	tempDir := t.TempDir()

	tests := []struct {
		name          string
		sampleRate    int
		channels      int
		bitsPerSample int
		audioFormat   int
		numFrames     int
	}{
		{name: "16-bit mono PCM 16kHz", sampleRate: 16000, channels: 1, bitsPerSample: 16, audioFormat: 1, numFrames: 160},
		{name: "16-bit stereo PCM 44.1kHz", sampleRate: 44100, channels: 2, bitsPerSample: 16, audioFormat: 1, numFrames: 200},
		{name: "24-bit mono PCM 48kHz", sampleRate: 48000, channels: 1, bitsPerSample: 24, audioFormat: 1, numFrames: 240},
		{name: "32-bit mono PCM 16kHz", sampleRate: 16000, channels: 1, bitsPerSample: 32, audioFormat: 1, numFrames: 100},
		{name: "32-bit float stereo 16kHz", sampleRate: 16000, channels: 2, bitsPerSample: 32, audioFormat: 3, numFrames: 100},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			wavPath := filepath.Join(tempDir, tt.name+".wav")
			createTestWAV(t, wavPath, tt.sampleRate, tt.channels, tt.bitsPerSample, tt.audioFormat, tt.numFrames)

			rate, samples, err := ReadWAVAudio(wavPath)
			if err != nil {
				t.Fatalf("ReadWAVAudio failed: %v", err)
			}
			if rate != tt.sampleRate {
				t.Errorf("sampleRate = %d, want %d", rate, tt.sampleRate)
			}
			if len(samples) != tt.numFrames {
				t.Errorf("len(samples) = %d, want %d", len(samples), tt.numFrames)
			}
			for i, s := range samples {
				if s < -1.0 || s > 1.0 {
					t.Errorf("sample[%d] = %f out of [-1.0, 1.0] range", i, s)
					break
				}
			}
		})
	}
}

func TestReadWAVAudioErrors(t *testing.T) {
	tempDir := t.TempDir()

	// 1. Non-existent file
	_, _, err := ReadWAVAudio(filepath.Join(tempDir, "nonexistent.wav"))
	if err == nil {
		t.Error("expected error for non-existent file")
	}

	// 2. File too short
	shortFile := filepath.Join(tempDir, "short.wav")
	_ = os.WriteFile(shortFile, []byte("RIFF"), 0o644)
	_, _, err = ReadWAVAudio(shortFile)
	if err == nil || !strings.Contains(err.Error(), "too short") {
		t.Errorf("expected too short error, got %v", err)
	}

	// 3. Not a RIFF file
	badMagicFile := filepath.Join(tempDir, "badmagic.wav")
	_ = os.WriteFile(badMagicFile, make([]byte, 100), 0o644)
	_, _, err = ReadWAVAudio(badMagicFile)
	if err == nil || !strings.Contains(err.Error(), "not a valid RIFF/WAVE") {
		t.Errorf("expected invalid header error, got %v", err)
	}

	// 4. Missing fmt chunk
	noFmtBuf := bytes.NewBufferString("RIFFxxxxWAVE")
	noFmtBuf.WriteString("data")
	binary.Write(noFmtBuf, binary.LittleEndian, uint32(8))
	noFmtBuf.Write(make([]byte, 8))
	noFmtFile := filepath.Join(tempDir, "nofmt.wav")
	_ = os.WriteFile(noFmtFile, noFmtBuf.Bytes(), 0o644)
	_, _, err = ReadWAVAudio(noFmtFile)
	if err == nil || !strings.Contains(err.Error(), "missing fmt chunk") {
		t.Errorf("expected missing fmt chunk error, got %v", err)
	}
}

func TestResolveSherpaModelDir(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	dataHome := filepath.Join(home, ".local", "share")
	t.Setenv("XDG_DATA_HOME", dataHome)

	sherpaDataDir := filepath.Join(dataHome, "mavor", "models", "sherpa", "parakeet-tdt-0.6b")
	if err := os.MkdirAll(sherpaDataDir, 0o755); err != nil {
		t.Fatal(err)
	}
	_ = os.WriteFile(filepath.Join(sherpaDataDir, "tokens.txt"), []byte("token1"), 0o644)
	_ = os.WriteFile(filepath.Join(sherpaDataDir, "encoder.onnx"), []byte("encoder"), 0o644)

	// 1. Resolve under XDG_DATA_HOME/mavor/models/sherpa/<model>
	cfg := config.Config{
		Model: "parakeet-tdt-0.6b",
	}
	resolved, err := ResolveSherpaModelDir(cfg)
	if err != nil {
		t.Fatalf("ResolveSherpaModelDir failed: %v", err)
	}
	if resolved != sherpaDataDir {
		t.Errorf("resolved = %q, want %q", resolved, sherpaDataDir)
	}

	// 2. Resolve under cfg.ModelDir/sherpa/<model>
	customModelDir := filepath.Join(t.TempDir(), "custom-models")
	zipformerDir := filepath.Join(customModelDir, "sherpa", "zipformer")
	_ = os.MkdirAll(zipformerDir, 0o755)
	_ = os.WriteFile(filepath.Join(zipformerDir, "tokens.txt"), []byte("tokens"), 0o644)
	_ = os.WriteFile(filepath.Join(zipformerDir, "model.onnx"), []byte("model"), 0o644)

	cfg = config.Config{
		Paths: config.Paths{Models: customModelDir},
		Model: "zipformer",
	}
	resolved, err = ResolveSherpaModelDir(cfg)
	if err != nil {
		t.Fatalf("ResolveSherpaModelDir with ModelDir failed: %v", err)
	}
	if resolved != zipformerDir {
		t.Errorf("resolved = %q, want %q", resolved, zipformerDir)
	}

	// 3. Resolve explicit absolute directory
	explicitDir := filepath.Join(t.TempDir(), "explicit-model")
	_ = os.MkdirAll(explicitDir, 0o755)
	cfg = config.Config{
		Model: explicitDir,
	}
	resolved, err = ResolveSherpaModelDir(cfg)
	if err != nil {
		t.Fatalf("ResolveSherpaModelDir with explicit dir failed: %v", err)
	}
	if resolved != explicitDir {
		t.Errorf("resolved = %q, want %q", resolved, explicitDir)
	}

	// 4. Missing model directory
	cfg = config.Config{
		Model: "nonexistent-model",
	}
	_, err = ResolveSherpaModelDir(cfg)
	if err == nil || !strings.Contains(err.Error(), "nonexistent-model") {
		t.Errorf("expected error mentioning nonexistent-model, got %v", err)
	}
}

func TestDetectSherpaModelType(t *testing.T) {
	tempDir := t.TempDir()

	// 1. Transducer (Parakeet-TDT)
	tdtDir := filepath.Join(tempDir, "parakeet")
	_ = os.MkdirAll(tdtDir, 0o755)
	_ = os.WriteFile(filepath.Join(tdtDir, "encoder.onnx"), []byte("1"), 0o644)
	_ = os.WriteFile(filepath.Join(tdtDir, "decoder.onnx"), []byte("1"), 0o644)
	_ = os.WriteFile(filepath.Join(tdtDir, "joiner.onnx"), []byte("1"), 0o644)
	_ = os.WriteFile(filepath.Join(tdtDir, "tokens.txt"), []byte("1"), 0o644)

	mtype, err := DetectSherpaModelType(tdtDir, "parakeet-tdt-0.6b")
	if err != nil || mtype != ModelTypeTransducer {
		t.Errorf("detect parakeet = %v, err=%v; want %v", mtype, err, ModelTypeTransducer)
	}

	// 2. Moonshine
	msDir := filepath.Join(tempDir, "moonshine")
	_ = os.MkdirAll(msDir, 0o755)
	_ = os.WriteFile(filepath.Join(msDir, "preprocess.onnx"), []byte("1"), 0o644)
	_ = os.WriteFile(filepath.Join(msDir, "encode.onnx"), []byte("1"), 0o644)
	_ = os.WriteFile(filepath.Join(msDir, "uncached_decode.onnx"), []byte("1"), 0o644)
	_ = os.WriteFile(filepath.Join(msDir, "cached_decode.onnx"), []byte("1"), 0o644)
	_ = os.WriteFile(filepath.Join(msDir, "tokens.txt"), []byte("1"), 0o644)

	mtype, err = DetectSherpaModelType(msDir, "moonshine-base")
	if err != nil || mtype != ModelTypeMoonshine {
		t.Errorf("detect moonshine = %v, err=%v; want %v", mtype, err, ModelTypeMoonshine)
	}

	// 3. Zipformer CTC
	zfDir := filepath.Join(tempDir, "zipformer")
	_ = os.MkdirAll(zfDir, 0o755)
	_ = os.WriteFile(filepath.Join(zfDir, "model.onnx"), []byte("1"), 0o644)
	_ = os.WriteFile(filepath.Join(zfDir, "tokens.txt"), []byte("1"), 0o644)

	mtype, err = DetectSherpaModelType(zfDir, "sherpa-onnx-zipformer-ctc")
	if err != nil || mtype != ModelTypeZipformerCTC {
		t.Errorf("detect zipformer = %v, err=%v; want %v", mtype, err, ModelTypeZipformerCTC)
	}

	// 4. SenseVoice
	svDir := filepath.Join(tempDir, "sensevoice")
	_ = os.MkdirAll(svDir, 0o755)
	_ = os.WriteFile(filepath.Join(svDir, "model.onnx"), []byte("1"), 0o644)
	_ = os.WriteFile(filepath.Join(svDir, "tokens.txt"), []byte("1"), 0o644)

	mtype, err = DetectSherpaModelType(svDir, "sensevoice-small")
	if err != nil || mtype != ModelTypeSenseVoice {
		t.Errorf("detect sensevoice = %v, err=%v; want %v", mtype, err, ModelTypeSenseVoice)
	}
}

func TestBuildSherpaOfflineConfig(t *testing.T) {
	tempDir := t.TempDir()
	modelDir := filepath.Join(tempDir, "parakeet-tdt")
	_ = os.MkdirAll(modelDir, 0o755)

	enc := filepath.Join(modelDir, "encoder.onnx")
	dec := filepath.Join(modelDir, "decoder.onnx")
	join := filepath.Join(modelDir, "joiner.onnx")
	tok := filepath.Join(modelDir, "tokens.txt")
	_ = os.WriteFile(enc, []byte("1"), 0o644)
	_ = os.WriteFile(dec, []byte("1"), 0o644)
	_ = os.WriteFile(join, []byte("1"), 0o644)
	_ = os.WriteFile(tok, []byte("1"), 0o644)

	cfg := config.Config{
		Model:    modelDir,
		Advanced: config.Advanced{Threads: 8},
	}

	sc, err := BuildSherpaOfflineConfig(cfg)
	if err != nil {
		t.Fatalf("BuildSherpaOfflineConfig failed: %v", err)
	}

	if sc.ModelType != ModelTypeTransducer {
		t.Errorf("ModelType = %v, want %v", sc.ModelType, ModelTypeTransducer)
	}
	if sc.Transducer.Encoder != enc {
		t.Errorf("Encoder = %q, want %q", sc.Transducer.Encoder, enc)
	}
	if sc.Transducer.Decoder != dec {
		t.Errorf("Decoder = %q, want %q", sc.Transducer.Decoder, dec)
	}
	if sc.Transducer.Joiner != join {
		t.Errorf("Joiner = %q, want %q", sc.Transducer.Joiner, join)
	}
	if sc.Tokens != tok {
		t.Errorf("Tokens = %q, want %q", sc.Tokens, tok)
	}
	if sc.NumThreads != 8 {
		t.Errorf("NumThreads = %d, want 8", sc.NumThreads)
	}
	if sc.Provider != "cpu" {
		t.Errorf("Provider = %q, want cpu — the vendored ONNX Runtime has no other", sc.Provider)
	}
}

func TestBuildSherpaOnlineConfig(t *testing.T) {
	tempDir := t.TempDir()
	modelDir := filepath.Join(tempDir, "online-transducer")
	_ = os.MkdirAll(modelDir, 0o755)

	enc := filepath.Join(modelDir, "encoder.onnx")
	dec := filepath.Join(modelDir, "decoder.onnx")
	join := filepath.Join(modelDir, "joiner.onnx")
	tok := filepath.Join(modelDir, "tokens.txt")
	_ = os.WriteFile(enc, []byte("1"), 0o644)
	_ = os.WriteFile(dec, []byte("1"), 0o644)
	_ = os.WriteFile(join, []byte("1"), 0o644)
	_ = os.WriteFile(tok, []byte("1"), 0o644)

	cfg := config.Config{
		Model:    modelDir,
		Advanced: config.Advanced{Threads: 4},
	}

	sc, err := BuildSherpaOnlineConfig(cfg)
	if err != nil {
		t.Fatalf("BuildSherpaOnlineConfig failed: %v", err)
	}

	if sc.Transducer.Encoder != enc || sc.Transducer.Decoder != dec || sc.Transducer.Joiner != join {
		t.Errorf("Transducer paths mismatch: %+v", sc.Transducer)
	}
	if sc.Tokens != tok {
		t.Errorf("Tokens = %q, want %q", sc.Tokens, tok)
	}
	if sc.NumThreads != 4 {
		t.Errorf("NumThreads = %d, want 4", sc.NumThreads)
	}
}

func TestSherpaTranscriberMockDispatch(t *testing.T) {
	tempDir := t.TempDir()
	modelDir := filepath.Join(tempDir, "fake-model")
	_ = os.MkdirAll(modelDir, 0o755)
	_ = os.WriteFile(filepath.Join(modelDir, "encoder.onnx"), []byte("1"), 0o644)
	_ = os.WriteFile(filepath.Join(modelDir, "decoder.onnx"), []byte("1"), 0o644)
	_ = os.WriteFile(filepath.Join(modelDir, "joiner.onnx"), []byte("1"), 0o644)
	_ = os.WriteFile(filepath.Join(modelDir, "tokens.txt"), []byte("1"), 0o644)

	wavPath := filepath.Join(tempDir, "audio.wav")
	createTestWAV(t, wavPath, 16000, 1, 16, 1, 320)

	cfg := config.Config{
		Model: modelDir,
	}

	st, err := NewSherpaTranscriber(cfg, slog.Default())
	if err != nil {
		t.Fatalf("NewSherpaTranscriber failed: %v", err)
	}

	mockRec := &MockSherpaRecognizer{
		Text: "hello from sherpa onnx",
	}

	st.RecognizerBuilder = func(_ config.Config, _ SherpaOfflineConfig, _ *slog.Logger) (SherpaRecognizer, error) {
		return mockRec, nil
	}

	ctx := context.Background()

	// 1. Transcribe automatically starts engine
	text, err := st.Transcribe(ctx, wavPath)
	if err != nil {
		t.Fatalf("Transcribe failed: %v", err)
	}
	if text != "hello from sherpa onnx" {
		t.Errorf("text = %q, want %q", text, "hello from sherpa onnx")
	}
	if mockRec.LastSampleRate != 16000 {
		t.Errorf("mock received sampleRate = %d, want 16000", mockRec.LastSampleRate)
	}
	if len(mockRec.LastSamples) != 320 {
		t.Errorf("mock received %d samples, want 320", len(mockRec.LastSamples))
	}
	if !st.IsStarted() {
		t.Error("expected transcriber to be marked as started")
	}

	// 2. Context cancellation during Transcribe
	cancellingCtx, cancel := context.WithCancel(context.Background())
	cancel()
	_, err = st.Transcribe(cancellingCtx, wavPath)
	if !errors.Is(err, context.Canceled) {
		t.Errorf("expected context.Canceled error, got %v", err)
	}

	// 3. Mock recognizer error
	mockRec.Err = errors.New("inference failed")
	_, err = st.Transcribe(ctx, wavPath)
	if err == nil || !strings.Contains(err.Error(), "inference failed") {
		t.Errorf("expected inference error, got %v", err)
	}

	// 4. Close releases resources
	if err := st.Close(); err != nil {
		t.Fatalf("Close failed: %v", err)
	}
	if !mockRec.Closed {
		t.Error("expected mock recognizer to be closed")
	}
	if st.IsStarted() {
		t.Error("expected transcriber to not be started after Close")
	}
}

func TestSherpaTranscriberStreamingLifecycle(t *testing.T) {
	tempDir := t.TempDir()
	modelDir := filepath.Join(tempDir, "fake-model")
	_ = os.MkdirAll(modelDir, 0o755)
	_ = os.WriteFile(filepath.Join(modelDir, "encoder.onnx"), []byte("1"), 0o644)
	_ = os.WriteFile(filepath.Join(modelDir, "decoder.onnx"), []byte("1"), 0o644)
	_ = os.WriteFile(filepath.Join(modelDir, "joiner.onnx"), []byte("1"), 0o644)
	_ = os.WriteFile(filepath.Join(modelDir, "tokens.txt"), []byte("1"), 0o644)

	cfg := config.Config{
		Model: modelDir,
	}

	st, err := NewSherpaTranscriber(cfg, slog.Default())
	if err != nil {
		t.Fatalf("NewSherpaTranscriber failed: %v", err)
	}

	mockRec := &MockSherpaRecognizer{
		StreamPartials: []string{"hello", "hello world"},
		StreamFinal:    "hello world final",
	}

	st.RecognizerBuilder = func(_ config.Config, _ SherpaOfflineConfig, _ *slog.Logger) (SherpaRecognizer, error) {
		return mockRec, nil
	}

	ctx := context.Background()

	// 1. Start stream
	if err := st.StartStream(ctx); err != nil {
		t.Fatalf("StartStream failed: %v", err)
	}

	// 2. Feed chunks
	chunk := make([]byte, 320)
	p1, err := st.FeedChunk(ctx, chunk)
	if err != nil || p1 != "hello" {
		t.Errorf("p1 = %q, err=%v, want %q", p1, err, "hello")
	}

	p2, err := st.FeedChunk(ctx, chunk)
	if err != nil || p2 != "hello world" {
		t.Errorf("p2 = %q, err=%v, want %q", p2, err, "hello world")
	}

	// 3. Stop stream
	final, err := st.StopStream(ctx)
	if err != nil || final != "hello world final" {
		t.Errorf("final = %q, err=%v, want %q", final, err, "hello world final")
	}
}

func TestSherpaTranscriberMissingModel(t *testing.T) {
	cfg := config.Config{
		Model: filepath.Join(t.TempDir(), "nonexistent"),
	}
	_, err := NewSherpaTranscriber(cfg, slog.Default())
	if err == nil {
		t.Fatal("expected error on missing model directory")
	}
	if !strings.Contains(err.Error(), "nonexistent") {
		t.Fatalf("error %q should mention nonexistent model", err)
	}
}

func TestSherpaTranscriberMissingWav(t *testing.T) {
	tempDir := t.TempDir()
	modelDir := filepath.Join(tempDir, "fake-model")
	_ = os.MkdirAll(modelDir, 0o755)
	_ = os.WriteFile(filepath.Join(modelDir, "encoder.onnx"), []byte("1"), 0o644)
	_ = os.WriteFile(filepath.Join(modelDir, "decoder.onnx"), []byte("1"), 0o644)
	_ = os.WriteFile(filepath.Join(modelDir, "joiner.onnx"), []byte("1"), 0o644)
	_ = os.WriteFile(filepath.Join(modelDir, "tokens.txt"), []byte("1"), 0o644)

	cfg := config.Config{
		Model: modelDir,
	}

	st, err := NewSherpaTranscriber(cfg, slog.Default())
	if err != nil {
		t.Fatal(err)
	}
	st.Recognizer = &MockSherpaRecognizer{Text: "mock"}
	st.started = true

	_, err = st.Transcribe(context.Background(), filepath.Join(tempDir, "missing.wav"))
	if err == nil {
		t.Fatal("expected error for missing wav file")
	}
}

func TestSherpaTranscriberStartIdempotent(t *testing.T) {
	tempDir := t.TempDir()
	modelDir := filepath.Join(tempDir, "fake-model")
	_ = os.MkdirAll(modelDir, 0o755)
	_ = os.WriteFile(filepath.Join(modelDir, "encoder.onnx"), []byte("1"), 0o644)
	_ = os.WriteFile(filepath.Join(modelDir, "decoder.onnx"), []byte("1"), 0o644)
	_ = os.WriteFile(filepath.Join(modelDir, "joiner.onnx"), []byte("1"), 0o644)
	_ = os.WriteFile(filepath.Join(modelDir, "tokens.txt"), []byte("1"), 0o644)

	cfg := config.Config{
		Model: modelDir,
	}

	st, err := NewSherpaTranscriber(cfg, slog.Default())
	if err != nil {
		t.Fatal(err)
	}

	initCount := 0
	st.RecognizerBuilder = func(_ config.Config, _ SherpaOfflineConfig, _ *slog.Logger) (SherpaRecognizer, error) {
		initCount++
		return &MockSherpaRecognizer{Text: "test"}, nil
	}

	ctx := context.Background()
	if err := st.Start(ctx); err != nil {
		t.Fatalf("first Start failed: %v", err)
	}
	if err := st.Start(ctx); err != nil {
		t.Fatalf("second Start failed: %v", err)
	}
	if initCount != 1 {
		t.Errorf("RecognizerBuilder called %d times, want 1", initCount)
	}

	_ = st.Close()
	_ = st.Close() // closing twice shouldn't panic
}

func TestBuildSherpaOfflineConfigArchitectures(t *testing.T) {
	// 1. Moonshine v1
	t.Run("Moonshine v1", func(t *testing.T) {
		tempDir := t.TempDir()
		_ = os.WriteFile(filepath.Join(tempDir, "preprocess.onnx"), []byte("1"), 0o644)
		_ = os.WriteFile(filepath.Join(tempDir, "encode.onnx"), []byte("1"), 0o644)
		_ = os.WriteFile(filepath.Join(tempDir, "uncached_decode.onnx"), []byte("1"), 0o644)
		_ = os.WriteFile(filepath.Join(tempDir, "cached_decode.onnx"), []byte("1"), 0o644)
		_ = os.WriteFile(filepath.Join(tempDir, "tokens.txt"), []byte("1"), 0o644)

		cfg := config.Config{Model: tempDir}
		sc, err := BuildSherpaOfflineConfig(cfg)
		if err != nil {
			t.Fatalf("Moonshine v1 config failed: %v", err)
		}
		if sc.ModelType != ModelTypeMoonshine {
			t.Errorf("ModelType = %v, want %v", sc.ModelType, ModelTypeMoonshine)
		}
		if sc.Moonshine.Encoder == "" || sc.Moonshine.UncachedDecoder == "" || sc.Moonshine.CachedDecoder == "" {
			t.Errorf("Moonshine paths missing: %+v", sc.Moonshine)
		}
	})

	// 2. Moonshine v2 (merged decoder)
	t.Run("Moonshine v2", func(t *testing.T) {
		tempDir := t.TempDir()
		_ = os.WriteFile(filepath.Join(tempDir, "encoder.onnx"), []byte("1"), 0o644)
		_ = os.WriteFile(filepath.Join(tempDir, "merged_decoder.onnx"), []byte("1"), 0o644)
		_ = os.WriteFile(filepath.Join(tempDir, "tokens.txt"), []byte("1"), 0o644)

		cfg := config.Config{Model: tempDir}
		sc, err := BuildSherpaOfflineConfig(cfg)
		if err != nil {
			t.Fatalf("Moonshine v2 config failed: %v", err)
		}
		if sc.Moonshine.MergedDecoder == "" {
			t.Errorf("expected merged decoder path, got %+v", sc.Moonshine)
		}
	})

	// 3. SenseVoice. A lone model.onnx beside a tokens.txt is also what a
	// NeMo CTC looks like, so the directory name is the only evidence — which
	// is the name `mavor models pull` gives it.
	t.Run("SenseVoice", func(t *testing.T) {
		tempDir := filepath.Join(t.TempDir(), "sensevoice-small")
		_ = os.MkdirAll(tempDir, 0o755)
		_ = os.WriteFile(filepath.Join(tempDir, "model.onnx"), []byte("1"), 0o644)
		_ = os.WriteFile(filepath.Join(tempDir, "tokens.txt"), []byte("1"), 0o644)

		cfg := config.Config{Model: tempDir}
		sc, err := BuildSherpaOfflineConfig(cfg)
		if err != nil {
			t.Fatalf("SenseVoice config failed: %v", err)
		}
		if sc.ModelType != ModelTypeSenseVoice {
			t.Errorf("ModelType = %v, want %v", sc.ModelType, ModelTypeSenseVoice)
		}
		if sc.SenseVoice.Model == "" {
			t.Errorf("SenseVoice Model path empty: %+v", sc.SenseVoice)
		}
	})

	// 4. Paraformer
	//
	// This subtest used to build a paraformer from an encoder and a decoder,
	// which is the streaming variant's layout. The offline artifact the
	// catalog downloads — sherpa-onnx-paraformer-zh-2024-03-09 — is a single
	// model.onnx beside a config.yaml, and populating the encoder/decoder
	// pair from an offline directory is what made every encoder-decoder model
	// look like a paraformer and fail on a missing lfr_window_size.
	t.Run("Paraformer", func(t *testing.T) {
		tempDir := t.TempDir()
		_ = os.WriteFile(filepath.Join(tempDir, "model.onnx"), []byte("1"), 0o644)
		_ = os.WriteFile(filepath.Join(tempDir, "config.yaml"), []byte("1"), 0o644)
		_ = os.WriteFile(filepath.Join(tempDir, "tokens.json"), []byte("1"), 0o644)
		_ = os.WriteFile(filepath.Join(tempDir, "tokens.txt"), []byte("1"), 0o644)

		cfg := config.Config{Model: tempDir}
		sc, err := BuildSherpaOfflineConfig(cfg)
		if err != nil {
			t.Fatalf("Paraformer config failed: %v", err)
		}
		if sc.Paraformer.Model == "" {
			t.Errorf("Paraformer model path missing: %+v", sc.Paraformer)
		}
	})

	// 5. Whisper — encoder and decoder, and genuinely so. Canary has the same
	// two files, so the name is what separates them.
	t.Run("Whisper", func(t *testing.T) {
		tempDir := filepath.Join(t.TempDir(), "whisper-onnx-base")
		_ = os.MkdirAll(tempDir, 0o755)
		_ = os.WriteFile(filepath.Join(tempDir, "encoder.onnx"), []byte("1"), 0o644)
		_ = os.WriteFile(filepath.Join(tempDir, "decoder.onnx"), []byte("1"), 0o644)
		_ = os.WriteFile(filepath.Join(tempDir, "tokens.txt"), []byte("1"), 0o644)

		cfgWhisper := config.Config{Model: tempDir}
		scWhisper, err := BuildSherpaOfflineConfig(cfgWhisper)
		if err != nil {
			t.Fatalf("Whisper config failed: %v", err)
		}
		if scWhisper.Whisper.Encoder == "" || scWhisper.Whisper.Decoder == "" {
			t.Errorf("Whisper paths missing: %+v", scWhisper.Whisper)
		}
	})
}

func TestBuildSherpaOfflineConfigErrors(t *testing.T) {
	tempDir := t.TempDir()

	// 1. Missing tokens.txt
	t.Run("Missing tokens", func(t *testing.T) {
		dir := filepath.Join(tempDir, "no-tokens")
		_ = os.MkdirAll(dir, 0o755)
		_ = os.WriteFile(filepath.Join(dir, "model.onnx"), []byte("1"), 0o644)

		cfg := config.Config{Model: dir}
		_, err := BuildSherpaOfflineConfig(cfg)
		if err == nil || !strings.Contains(err.Error(), "tokens") {
			t.Errorf("expected missing tokens error, got %v", err)
		}
	})

	// 2. A transducer that is missing its joiner is not read as a broken
	// transducer: the three files together are what identifies one, so two of
	// them is an encoder-decoder model. There is no longer a key that can
	// force the transducer reader onto a directory that cannot feed it, which
	// matters because sherpa-onnx aborts the process rather than erroring
	// when it is handed the wrong layout.
	t.Run("Encoder and decoder with no joiner is not a transducer", func(t *testing.T) {
		dir := filepath.Join(tempDir, "no-joiner")
		_ = os.MkdirAll(dir, 0o755)
		_ = os.WriteFile(filepath.Join(dir, "encoder.onnx"), []byte("1"), 0o644)
		_ = os.WriteFile(filepath.Join(dir, "decoder.onnx"), []byte("1"), 0o644)
		_ = os.WriteFile(filepath.Join(dir, "tokens.txt"), []byte("1"), 0o644)

		sc, err := BuildSherpaOfflineConfig(config.Config{Model: dir})
		if err != nil {
			t.Fatalf("BuildSherpaOfflineConfig: %v", err)
		}
		if sc.ModelType == ModelTypeTransducer {
			t.Error("ModelType = transducer for a directory with no joiner")
		}
		if sc.Transducer.Encoder != "" {
			t.Errorf("the transducer fields were populated anyway: %+v", sc.Transducer)
		}
	})

	// 3. Moonshine missing encoder
	t.Run("Moonshine missing encoder", func(t *testing.T) {
		dir := filepath.Join(tempDir, "ms-no-enc")
		_ = os.MkdirAll(dir, 0o755)
		_ = os.WriteFile(filepath.Join(dir, "merged_decoder.onnx"), []byte("1"), 0o644)
		_ = os.WriteFile(filepath.Join(dir, "tokens.txt"), []byte("1"), 0o644)

		cfg := config.Config{Model: dir}
		_, err := BuildSherpaOfflineConfig(cfg)
		if err == nil || !strings.Contains(err.Error(), "encoder") {
			t.Errorf("expected missing encoder error, got %v", err)
		}
	})
}

// The user never chooses a decoding method, and there is no key that sets
// one. Greedy search is what runs, on every model and both recognizers: on
// LibriSpeech the zipformer transducer scores 2.17% word error rate greedy
// against 2.15% with modified beam search, and every non-transducer model
// aborts on anything else. Beam search exists only to honour hotwords, and
// configuring [vocabulary] is what will turn it on — see
// docs/design/configuration-surface.md §7. That mapping is not built yet, so
// nothing here asks for hotwords and nothing is passed.
func TestDecodingIsGreedyWithNothingToBias(t *testing.T) {
	dir := t.TempDir()
	for _, f := range []string{"encoder.onnx", "decoder.onnx", "joiner.onnx", "tokens.txt"} {
		if err := os.WriteFile(filepath.Join(dir, f), []byte("x"), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	cfg := config.Config{Model: dir}

	online, err := BuildSherpaOnlineConfig(cfg)
	if err != nil {
		t.Fatal(err)
	}
	if online.DecodingMethod != "greedy_search" {
		t.Errorf("online DecodingMethod = %q, want greedy_search", online.DecodingMethod)
	}
	if online.HotwordsFile != "" {
		t.Errorf("online HotwordsFile = %q, want none", online.HotwordsFile)
	}

	offline, err := BuildSherpaOfflineConfig(cfg)
	if err != nil {
		t.Fatal(err)
	}
	if offline.DecodingMethod != "greedy_search" {
		t.Errorf("offline DecodingMethod = %q, want greedy_search", offline.DecodingMethod)
	}
	if offline.HotwordsFile != "" {
		t.Errorf("offline HotwordsFile = %q, want none", offline.HotwordsFile)
	}
}
