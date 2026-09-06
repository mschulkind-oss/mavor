package daemon

import (
	"context"
	"encoding/binary"
	"testing"
	"time"

	"github.com/mschulkind-oss/mavor/internal/audio"
	"github.com/mschulkind-oss/mavor/internal/output"
	"github.com/mschulkind-oss/mavor/internal/overlay"
	"github.com/mschulkind-oss/mavor/internal/speech"
)

// pcmChunk builds one 30 ms-ish frame of PCM at a fixed amplitude. Zero is
// silence to the VAD; anything loud enough counts as speech.
func pcmChunk(amplitude int16, samples int) []byte {
	buf := make([]byte, samples*2)
	for i := 0; i < samples; i++ {
		binary.LittleEndian.PutUint16(buf[i*2:], uint16(amplitude))
	}
	return buf
}

// speechThenSilence is the chunk sequence a phrase boundary needs: enough
// speech to satisfy min_phrase_ms, then enough silence to satisfy pause_ms,
// then nothing.
func speechThenSilence(speechFrames, silenceFrames int) [][]byte {
	var out [][]byte
	for i := 0; i < speechFrames; i++ {
		out = append(out, pcmChunk(12000, 480))
	}
	for i := 0; i < silenceFrames; i++ {
		out = append(out, pcmChunk(0, 480))
	}
	return out
}

func waitForOverlayText(t *testing.T, ov *overlay.Mock, want string) {
	t.Helper()
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		for _, got := range ov.Texts() {
			if got == want {
				return
			}
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatalf("overlay never showed %q, saw %v", want, ov.Texts())
}

// The companion paints the overlay and nothing else. The output emitter has
// one writer — the final transcription — so the text the user gets is the main
// model's, and the companion never influences it (§10.3).
func TestCompanionPreviewNeverReachesTheOutputEmitter(t *testing.T) {
	companion := speech.NewMockStreamTranscriber("companion final", "com", "companion partial")
	ov := &overlay.Mock{}
	out := &output.Mock{}

	d, sock := newTestDaemon(t, func(c *Config) {
		c.Recorder = &audio.MockRecorder{FixturePath: "/tmp/fake.wav", ChunkData: []byte{1, 2, 3, 4}}
		c.Transcriber = &speech.Mock{Text: "the real transcript"}
		c.Overlay = ov
		c.Output = out
		c.PreviewMode = speech.PreviewCompanion
		c.PreviewCompanion = companion
	})
	stop := runDaemon(t, d)
	defer stop()

	sendWithRetry(t, sock, "toggle")
	waitForState(t, sock, "recording")
	waitForOverlayText(t, ov, "companion partial")

	if calls := out.Calls(); len(calls) != 0 {
		t.Fatalf("preview emitted output while recording: %v", calls)
	}

	sendWithRetry(t, sock, "toggle")
	waitForState(t, sock, "idle")

	calls := out.Calls()
	if len(calls) != 1 || calls[0] != "the real transcript" {
		t.Fatalf("output.Calls = %v, want exactly the main model's transcript", calls)
	}
	for _, c := range calls {
		if c == "companion final" || c == "companion partial" || c == "com" {
			t.Fatalf("companion text %q reached the output emitter", c)
		}
	}
}

// In companion mode the main model is not asked for partials: it is the model
// that produces the final text, once, and feeding it chunks would be a second
// decode nobody asked for.
func TestCompanionModeDoesNotStreamTheMainModel(t *testing.T) {
	main := speech.NewMockStreamTranscriber("final transcript", "main partial")
	companion := speech.NewMockStreamTranscriber("companion final", "companion partial")
	ov := &overlay.Mock{}

	d, sock := newTestDaemon(t, func(c *Config) {
		c.Recorder = &audio.MockRecorder{FixturePath: "/tmp/fake.wav", ChunkData: []byte{1, 2, 3, 4}}
		c.Transcriber = main
		c.Overlay = ov
		c.PreviewMode = speech.PreviewCompanion
		c.PreviewCompanion = companion
	})
	stop := runDaemon(t, d)
	defer stop()

	sendWithRetry(t, sock, "toggle")
	waitForState(t, sock, "recording")
	waitForOverlayText(t, ov, "companion partial")

	if got := main.Chunks(); len(got) != 0 {
		t.Fatalf("main model received %d audio chunks in companion mode, want 0", len(got))
	}
	if got := companion.Chunks(); len(got) == 0 {
		t.Fatal("companion received no audio chunks")
	}
}

// `preview.source = "phrases"` forces phrase mode even when the main model
// could decode incrementally.
func TestExplicitPhraseModeDoesNotStreamAStreamingModel(t *testing.T) {
	main := speech.NewMockStreamTranscriber("final transcript", "streamed partial")

	d, sock := newTestDaemon(t, func(c *Config) {
		c.Recorder = &audio.MockRecorder{FixturePath: "/tmp/fake.wav", ChunkData: []byte{1, 2, 3, 4}}
		c.Transcriber = main
		c.PreviewMode = speech.PreviewPhrases
	})
	stop := runDaemon(t, d)
	defer stop()

	sendWithRetry(t, sock, "toggle")
	waitForState(t, sock, "recording")
	time.Sleep(150 * time.Millisecond)

	if main.IsStreaming() {
		t.Fatal("phrase mode started a streaming session on the main model")
	}
	if got := main.Chunks(); len(got) != 0 {
		t.Fatalf("phrase mode fed %d chunks to the streaming API, want 0", len(got))
	}
}

// Phrase mode: on a pause, the audio since the last pause is transcribed with
// the MAIN model and appended to the preview — and still never typed.
func TestPhraseModePaintsThePreviewAndEmitsOnce(t *testing.T) {
	rec := &audio.MockRecorder{FixturePath: "/tmp/fake.wav"}
	rec.SetChunks(speechThenSilence(4, 20)...)
	ov := &overlay.Mock{}
	out := &output.Mock{}

	d, sock := newTestDaemon(t, func(c *Config) {
		c.Recorder = rec
		c.Transcriber = &speech.Mock{Text: "hello world"}
		c.Overlay = ov
		c.Output = out
		c.PreviewMode = speech.PreviewPhrases
		c.MinPhraseDuration = 60 * time.Millisecond
		c.SilenceThreshold = 60 * time.Millisecond
	})
	stop := runDaemon(t, d)
	defer stop()

	sendWithRetry(t, sock, "toggle")
	waitForState(t, sock, "recording")
	waitForOverlayText(t, ov, "hello world")

	if calls := out.Calls(); len(calls) != 0 {
		t.Fatalf("phrase preview emitted output while recording: %v", calls)
	}

	sendWithRetry(t, sock, "toggle")
	waitForState(t, sock, "idle")

	if calls := out.Calls(); len(calls) != 1 {
		t.Fatalf("output emitted %d times over the cycle, want exactly 1: %v", len(calls), calls)
	}
}

// §10.3: on stop, all preview work is cancelled and its results discarded —
// including a phrase transcription still in flight. A result that arrives
// afterwards is dropped, not appended.
func TestPhraseResultArrivingAfterStopIsDropped(t *testing.T) {
	ov := &overlay.Mock{}
	d, _ := newTestDaemon(t, func(c *Config) {
		c.Overlay = ov
		c.Recorder = &audio.MockRecorder{FixturePath: "/tmp/fake.wav"}
		c.PreviewMode = speech.PreviewPhrases
	})

	ctx, cancel := context.WithCancel(t.Context())
	defer cancel()
	d.startStreamingMonitoring(ctx)

	d.streamMu.Lock()
	gen := d.streamGen
	d.streamMu.Unlock()

	d.appendPhrase(ctx, gen, "during the recording")
	if got := ov.LastText(); got != "during the recording" {
		t.Fatalf("overlay text = %q, want the phrase painted while recording", got)
	}

	d.stopStreamingMonitoring()

	// The transcription that was still decoding when the user released the
	// key lands here.
	d.appendPhrase(ctx, gen, "arrived too late")

	if got := ov.LastText(); got != "" {
		t.Fatalf("overlay text = %q, want it cleared and left cleared", got)
	}
	for _, txt := range ov.Texts() {
		if txt == "arrived too late" || txt == "during the recording arrived too late" {
			t.Fatalf("a phrase result was appended after stop: %v", ov.Texts())
		}
	}
}

// The same rule for the other mechanism: a partial that arrives after stop
// paints nothing. The overlay has one writer, and only between start and stop.
func TestStreamPartialArrivingAfterStopIsDropped(t *testing.T) {
	ov := &overlay.Mock{}
	companion := speech.NewMockStreamTranscriber("companion final")
	d, _ := newTestDaemon(t, func(c *Config) {
		c.Overlay = ov
		c.Recorder = &audio.MockRecorder{FixturePath: "/tmp/fake.wav"}
		c.PreviewMode = speech.PreviewCompanion
		c.PreviewCompanion = companion
	})

	ctx, cancel := context.WithCancel(t.Context())
	defer cancel()
	d.startStreamingMonitoring(ctx)

	d.streamMu.Lock()
	gen := d.streamGen
	d.streamMu.Unlock()

	d.stopStreamingMonitoring()
	d.setPreview(ctx, gen, "late partial")

	for _, txt := range ov.Texts() {
		if txt == "late partial" {
			t.Fatalf("a partial painted the overlay after stop: %v", ov.Texts())
		}
	}
}
