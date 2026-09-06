//go:build integration

package integration

import (
	"testing"
	"time"

	"github.com/mschulkind-oss/mavor/internal/ipc"
)

// TestCannedWAVReachesClipboard exercises the full daemon pipeline against a
// real PipeWire null-sink and a fake whisper-cli shim. It proves:
//   - parec captures audio from the configured PULSE_SOURCE
//   - the FSM cycles Idle → Recording → Transcribing → Idle
//   - the transcribed text reaches the clipboard via wl-copy
//
// The transcription itself is faked (deterministic transcript) so the test
// doesn't need a downloaded model; the end-to-end test with real whisper is
// in the e2e build tag.
func TestCannedWAVReachesClipboard(t *testing.T) {
	const transcript = "the quick brown fox jumps over the lazy dog"
	sinkName := "mavor-test-" + sanitize(t.Name())
	h := Start(t, Options{
		Width:          testWidth,
		Height:         testHeight,
		AudioSink:      sinkName,
		FakeTranscript: transcript,
	})
	socket, _ := h.RunDaemon(t.Context(), MavorBinary, "whisper-tiny.en")

	if r, err := ipc.Send(socket, ipc.Request{Action: "toggle"}, 2*time.Second); err != nil {
		t.Fatalf("toggle to record: %v", err)
	} else if r.State != "recording" {
		t.Fatalf("first toggle returned %q, want recording", r.State)
	}

	// Inject a half-second of audio into the null sink so parec captures
	// real WAV bytes (an empty WAV would trip our recorder's size check).
	pipeAudio(t, h, sinkName)

	if r, err := ipc.Send(socket, ipc.Request{Action: "toggle"}, 2*time.Second); err != nil {
		t.Fatalf("toggle to transcribe: %v", err)
	} else if r.State != "transcribing" {
		t.Fatalf("second toggle returned %q, want transcribing", r.State)
	}

	// Wait for the daemon to return to idle (transcription pipeline done).
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		if r, err := ipc.Send(socket, ipc.Request{Action: "status"}, 500*time.Millisecond); err == nil && r.State == "idle" {
			break
		}
		time.Sleep(20 * time.Millisecond)
	}

	// wl-copy is asynchronous — give it a moment to land before we read.
	got := waitForClipboard(t, h, transcript, 2*time.Second)
	if got != transcript {
		t.Fatalf("clipboard = %q, want %q", got, transcript)
	}
}

func waitForClipboard(t *testing.T, h *Harness, want string, timeout time.Duration) string {
	t.Helper()
	deadline := time.Now().Add(timeout)
	var last string
	for time.Now().Before(deadline) {
		last = h.WlPaste()
		if last == want {
			return last
		}
		time.Sleep(50 * time.Millisecond)
	}
	return last
}
