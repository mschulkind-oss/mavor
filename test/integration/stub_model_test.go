//go:build integration

package integration

import (
	"testing"
	"time"

	"github.com/mschulkind-oss/mavor/internal/ipc"
)

// Overlay/lifecycle tests use a stub model, not real inference. Turning the
// production silence filter off must not accidentally run Whisper on that stub.
func TestStubModelRecordingStopsWithoutInferenceError(t *testing.T) {
	h := Start(t, Options{Width: testWidth, Height: testHeight})
	socket, _ := h.RunDaemon(t.Context(), MavorBinary, "whisper-tiny.en")
	if h.ShimDir == "" {
		t.Fatal("stub model must use the deterministic empty-transcript shim, not real Whisper")
	}
	h.ShowOverlay(socket)
	// Let parec initialize the recording before stop; this test concerns the
	// transcriber stub, not interruption before the capture file is created.
	time.Sleep(100 * time.Millisecond)
	if _, err := ipc.Send(socket, ipc.Request{Action: "stop"}, 2*time.Second); err != nil {
		t.Fatal(err)
	}
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		state, err := h.DaemonState(socket)
		if err != nil {
			t.Fatal(err)
		}
		if state == "idle" {
			// HideOverlay must wait for the previous cycle before ShowOverlay
			// sends another toggle; otherwise it can cancel rather than start.
			h.ShowOverlay(socket)
			time.Sleep(100 * time.Millisecond)
			h.HideOverlay(socket)
			if got, err := h.DaemonState(socket); err != nil || got != "idle" {
				t.Fatalf("HideOverlay returned before idle: state=%q, error=%v", got, err)
			}
			return
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatal("stub-model cycle never completed")
}
