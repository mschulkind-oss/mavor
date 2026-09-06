//go:build e2e

package integration

import (
	"testing"
	"time"

	"github.com/mschulkind-oss/mavor/internal/ipc"
)

// TestEndToEndRealWhisper runs the entire dictation pipeline without mocks or shims,
// verifying that parec -> real whisper-cli -> wtype/wl-copy executes and returns to idle.
func TestEndToEndRealWhisper(t *testing.T) {
	sinkName := "mavor-test-" + sanitize(t.Name())
	h := Start(t, Options{
		Width:     testWidth,
		Height:    testHeight,
		AudioSink: sinkName,
		// No FakeTranscript: drives real whisper-cli with whisper-tiny.en model
	})
	socket, _ := h.RunDaemon(t.Context(), MavorBinary, "whisper-tiny.en")

	if r, err := ipc.Send(socket, ipc.Request{Action: "toggle"}, 2*time.Second); err != nil {
		t.Fatalf("toggle to record: %v", err)
	} else if r.State != "recording" {
		t.Fatalf("first toggle returned %q, want recording", r.State)
	}

	// Inject a half-second tone into the null sink so parec writes valid audio.
	pipeAudio(t, h, sinkName)

	if r, err := ipc.Send(socket, ipc.Request{Action: "toggle"}, 2*time.Second); err != nil {
		t.Fatalf("toggle to transcribe: %v", err)
	} else if r.State != "transcribing" {
		t.Fatalf("second toggle returned %q, want transcribing", r.State)
	}

	// Wait for the real whisper execution to finish and return to idle.
	deadline := time.Now().Add(15 * time.Second)
	for time.Now().Before(deadline) {
		if r, err := ipc.Send(socket, ipc.Request{Action: "status"}, 500*time.Millisecond); err == nil && r.State == "idle" {
			t.Log("real whisper-cli finished and daemon returned to idle")
			return
		}
		time.Sleep(100 * time.Millisecond)
	}

	t.Fatalf("timed out waiting for real whisper transcription to return to idle")
}
