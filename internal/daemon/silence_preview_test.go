package daemon

import (
	"context"
	"errors"
	"path/filepath"
	"testing"
	"time"

	"github.com/mschulkind-oss/mavor/internal/audio"
	"github.com/mschulkind-oss/mavor/internal/output"
	"github.com/mschulkind-oss/mavor/internal/speech"
)

// A quiet 90ms recording fails both the amplitude and duration requirements
// of the energy filter. Recognition, not microphone gain, must decide whether
// the final model gets to hear it.
func quietRecording(t *testing.T) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "quiet.wav")
	samples := make([]int16, audio.DefaultSampleRate*90/1000)
	for i := range samples {
		samples[i] = 32
	}
	if err := audio.WriteWAV(path, samples, audio.DefaultSampleRate); err != nil {
		t.Fatal(err)
	}
	if detected, err := audio.DetectSpeech(path, 150*time.Millisecond); err != nil || detected {
		t.Fatalf("fixture must fail energy filter: detected=%v, err=%v", detected, err)
	}
	return path
}

func TestQuietRecordingWithPreviewRunsFinalTranscription(t *testing.T) {
	for _, tc := range []struct {
		name      string
		mode      speech.PreviewMode
		partial   string
		phrase    string
		final     string
		mainText  string
		stale     bool
		cancelled bool
		stopErr   error
		wantCalls int
	}{
		{name: "companion partial", mode: speech.PreviewCompanion, partial: "preview words", mainText: "final words", wantCalls: 1},
		{name: "main model partial", mode: speech.PreviewMainModel, partial: "preview words", mainText: "final words", wantCalls: 1},
		{name: "phrase", mode: speech.PreviewPhrases, phrase: "preview words", mainText: "final words", wantCalls: 1},
		{name: "companion final only", mode: speech.PreviewCompanion, final: "preview tail", mainText: "final words", wantCalls: 1},
		{name: "main model final only", mode: speech.PreviewMainModel, final: "preview tail", mainText: "final words", wantCalls: 1},
		{name: "partial later cleared", mode: speech.PreviewCompanion, partial: "preview words", mainText: "final words", wantCalls: 1},
		{name: "empty final transcript", mode: speech.PreviewCompanion, partial: "preview words", mainText: ""},
		{name: "no preview words", mode: speech.PreviewCompanion, mainText: "must not emit"},
		{name: "partial annotation", mode: speech.PreviewCompanion, partial: "[BLANK_AUDIO]", mainText: "must not emit"},
		{name: "phrase annotation", mode: speech.PreviewPhrases, phrase: "[BLANK_AUDIO]", mainText: "must not emit"},
		{name: "failed tail decode", mode: speech.PreviewCompanion, final: "preview tail", stopErr: errors.New("decode failed"), mainText: "must not emit"},
		{name: "final annotation", mode: speech.PreviewCompanion, final: "[BLANK_AUDIO]", mainText: "must not emit"},
		{name: "whitespace", mode: speech.PreviewCompanion, partial: " \n\t ", final: " \n ", mainText: "must not emit"},
		{name: "stale partial", mode: speech.PreviewCompanion, partial: "old preview", stale: true, mainText: "must not emit"},
		{name: "cancelled partial", mode: speech.PreviewCompanion, partial: "cancelled preview", cancelled: true, mainText: "must not emit"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			out := &output.Mock{}
			main := &timedTranscriber{Mock: speech.Mock{Text: tc.mainText}, called: make(chan time.Time, 1)}
			stream := speech.NewMockStreamTranscriber(tc.final)
			stream.SetErrors(nil, nil, tc.stopErr)
			d, sock := newTestDaemon(t, func(c *Config) {
				c.Recorder = &audio.MockRecorder{FixturePath: quietRecording(t)}
				c.Transcriber = main
				c.Output = out
				c.PreviewMode = tc.mode
				if tc.mode == speech.PreviewMainModel {
					c.Transcriber = &previewAndFinalTranscriber{timedTranscriber: main, MockStreamTranscriber: stream}
				} else {
					c.PreviewCompanion = stream
				}
			})
			stop := runDaemon(t, d)
			defer stop()
			sendWithRetry(t, sock, "toggle")
			d.streamMu.Lock()
			gen := d.streamGen
			d.streamMu.Unlock()
			if tc.stale {
				gen--
			}
			ctx, cancel := context.WithCancel(t.Context())
			defer cancel()
			if tc.cancelled {
				cancel()
			}
			if tc.partial != "" {
				d.setPreview(ctx, gen, tc.partial)
			}
			if tc.name == "partial later cleared" {
				d.setPreview(ctx, gen, "")
			}
			if tc.phrase != "" {
				d.appendPhrase(ctx, gen, tc.phrase)
			}
			sendWithRetry(t, sock, "toggle")
			waitForState(t, sock, "idle")
			calls := out.Calls()
			if len(calls) != tc.wantCalls {
				t.Fatalf("output = %v, want %d final transcripts", calls, tc.wantCalls)
			}
			if len(calls) != 0 && calls[0] != tc.mainText {
				t.Fatalf("output = %v, want only main model text %q", calls, tc.mainText)
			}
			wantTranscribe := tc.wantCalls == 1 || tc.name == "empty final transcript"
			if got := len(main.called) != 0; got != wantTranscribe {
				t.Fatalf("final model called = %v, want %v", got, wantTranscribe)
			}
		})
	}
}

type previewAndFinalTranscriber struct {
	*timedTranscriber
	*speech.MockStreamTranscriber
}

// Override the embedded stream mock's batch method: the output-producing
// transcription must remain independently observable and different from preview.
func (t *previewAndFinalTranscriber) Transcribe(ctx context.Context, path string) (string, error) {
	return t.timedTranscriber.Transcribe(ctx, path)
}

func TestPreviewSpeechEvidenceDoesNotLeakToNextRecording(t *testing.T) {
	rec := &audio.MockRecorder{FixturePath: quietRecording(t)}
	out := &output.Mock{}
	d, sock := newTestDaemon(t, func(c *Config) {
		c.Recorder = rec
		c.Output = out
		c.PreviewMode = speech.PreviewPhrases
	})
	stop := runDaemon(t, d)
	defer stop()
	for round := 0; round < 2; round++ {
		if round > 0 {
			rec.FixturePath = quietRecording(t)
		}
		sendWithRetry(t, sock, "toggle")
		d.streamMu.Lock()
		gen := d.streamGen
		d.streamMu.Unlock()
		if round == 0 {
			d.appendPhrase(t.Context(), gen, "first recording only")
		}
		sendWithRetry(t, sock, "toggle")
		waitForState(t, sock, "idle")
	}
	if calls := out.Calls(); len(calls) != 1 {
		t.Fatalf("output = %v, want only first recording transcribed", calls)
	}
}

// Hold the tail decode until the test releases it. This makes ordering checks
// independent of machine speed instead of relying on sleeps.
type heldPreviewTail struct {
	*speech.MockStreamTranscriber
	entered chan struct{}
	release chan struct{}
}

func (s *heldPreviewTail) StopStream(ctx context.Context) (string, error) {
	close(s.entered)
	<-s.release
	return s.MockStreamTranscriber.StopStream(ctx)
}

func TestQuietPreviewDoesNotWaitForTailWhenWordsAlreadyRecognized(t *testing.T) {
	for _, partial := range []bool{true, false} {
		name := "tail only"
		if partial {
			name = "partial already recognized"
		}
		t.Run(name, func(t *testing.T) {
			companion := &heldPreviewTail{
				MockStreamTranscriber: speech.NewMockStreamTranscriber("tail preview"),
				entered:               make(chan struct{}),
				release:               make(chan struct{}),
			}
			main := &timedTranscriber{Mock: speech.Mock{Text: "final words"}, called: make(chan time.Time, 1)}
			out := &output.Mock{}
			d, sock := newTestDaemon(t, func(c *Config) {
				c.Recorder = &audio.MockRecorder{FixturePath: quietRecording(t)}
				c.Transcriber = main
				c.Output = out
				c.PreviewMode = speech.PreviewCompanion
				c.PreviewCompanion = companion
			})
			stop := runDaemon(t, d)
			defer stop()
			// Always release the held goroutine, including on assertion failure.
			released := false
			defer func() {
				if !released {
					close(companion.release)
				}
				d.awaitStreamDrain(t.Context())
			}()
			sendWithRetry(t, sock, "toggle")
			if partial {
				d.streamMu.Lock()
				gen := d.streamGen
				d.streamMu.Unlock()
				d.setPreview(t.Context(), gen, "recognized partial")
			}
			// IPC must not wait for the tail, even for a quiet recording.
			sendWithRetry(t, sock, "toggle")
			select {
			case <-companion.entered:
			case <-time.After(sendTimeout):
				t.Fatal("tail decode did not start")
			}
			if partial {
				select {
				case <-main.called:
				case <-time.After(sendTimeout):
					t.Fatal("final transcription waited for a tail despite recognized partials")
				}
			} else {
				// Wait for the pipeline to evaluate the file, not just the tail
				// goroutine. It must not reject the recording while recognition
				// is still finishing.
				ctx, cancel := context.WithCancel(t.Context())
				cancel()
				if d.previewRecognizedSpeech(ctx) {
					t.Fatal("unexpected speech evidence before the held decode completed")
				}
				if got := sendWithRetry(t, sock, "status").State; got != "transcribing" {
					t.Fatalf("state while tail pending = %q, want transcribing", got)
				}
			}
			close(companion.release)
			released = true
			waitForState(t, sock, "idle")
			if calls := out.Calls(); len(calls) != 1 || calls[0] != "final words" {
				t.Fatalf("output = %v, want only final model words", calls)
			}
		})
	}
}
