package daemon

import (
	"context"
	"errors"
	"io"
	"log/slog"
	"path/filepath"
	"testing"
	"time"

	"github.com/mschulkind-oss/mavor/internal/audio"
	"github.com/mschulkind-oss/mavor/internal/history"
	"github.com/mschulkind-oss/mavor/internal/ipc"
	"github.com/mschulkind-oss/mavor/internal/output"
	"github.com/mschulkind-oss/mavor/internal/overlay"
	"github.com/mschulkind-oss/mavor/internal/speech"
)

const sendTimeout = 2 * time.Second

func newTestDaemon(t *testing.T, opts ...func(*Config)) (*Daemon, string) {
	t.Helper()
	sock := filepath.Join(t.TempDir(), "mavor.sock")
	cfg := Config{
		Socket:        sock,
		Recorder:      &audio.MockRecorder{FixturePath: "/tmp/fake.wav"},
		Transcriber:   &speech.Mock{Text: "hello world"},
		Output:        &output.Mock{},
		Overlay:       &overlay.Noop{},
		Logger:        slog.New(slog.NewTextHandler(io.Discard, nil)),
		ErrorDuration: 1 * time.Millisecond,
	}
	for _, o := range opts {
		o(&cfg)
	}
	return New(cfg), sock
}

func runDaemon(t *testing.T, d *Daemon) (cancel func()) {
	t.Helper()
	ctx, cancelFn := context.WithCancel(t.Context())
	done := make(chan error, 1)
	go func() { done <- d.Run(ctx) }()
	return func() {
		cancelFn()
		select {
		case <-done:
		case <-time.After(2 * time.Second):
			t.Fatal("daemon did not exit after cancel")
		}
	}
}

func sendWithRetry(t *testing.T, sock, action string) ipc.Response {
	t.Helper()
	deadline := time.Now().Add(sendTimeout)
	for {
		resp, err := ipc.Send(sock, ipc.Request{Action: action}, 200*time.Millisecond)
		if err == nil {
			return resp
		}
		if time.Now().After(deadline) {
			t.Fatalf("send %s: %v", action, err)
		}
		time.Sleep(10 * time.Millisecond)
	}
}

func waitForState(t *testing.T, sock, want string) {
	t.Helper()
	deadline := time.Now().Add(sendTimeout)
	for time.Now().Before(deadline) {
		if got := sendWithRetry(t, sock, "status"); got.State == want {
			return
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatalf("never reached state %q", want)
}

func TestToggleDrivesFullPipeline(t *testing.T) {
	out := &output.Mock{}
	d, sock := newTestDaemon(t, func(c *Config) { c.Output = out })
	stop := runDaemon(t, d)
	defer stop()

	// First toggle: Idle -> Recording
	resp := sendWithRetry(t, sock, "toggle")
	if resp.State != "recording" {
		t.Fatalf("first toggle returned state %q, want recording", resp.State)
	}

	// Second toggle: Recording -> Transcribing -> (async pipeline) -> Idle
	resp = sendWithRetry(t, sock, "toggle")
	if resp.State != "transcribing" {
		t.Fatalf("second toggle returned state %q, want transcribing", resp.State)
	}

	waitForState(t, sock, "idle")

	calls := out.Calls()
	if len(calls) != 1 || calls[0] != "hello world" {
		t.Fatalf("output.Calls = %v, want [\"hello world\"]", calls)
	}
}

func TestUnknownActionReturnsError(t *testing.T) {
	d, sock := newTestDaemon(t)
	stop := runDaemon(t, d)
	defer stop()

	resp := sendWithRetry(t, sock, "explode")
	if resp.Error == "" {
		t.Fatal("expected error response for unknown action")
	}
}

func TestStatusActionExposesState(t *testing.T) {
	d, sock := newTestDaemon(t)
	stop := runDaemon(t, d)
	defer stop()

	if got := sendWithRetry(t, sock, "status"); got.State != "idle" {
		t.Fatalf("initial status = %q, want idle", got.State)
	}
	sendWithRetry(t, sock, "toggle")
	if got := sendWithRetry(t, sock, "status"); got.State != "recording" {
		t.Fatalf("after toggle status = %q, want recording", got.State)
	}
}

func TestOverlayTracksStateChanges(t *testing.T) {
	ov := &overlay.Noop{}
	d, sock := newTestDaemon(t, func(c *Config) { c.Overlay = ov })
	stop := runDaemon(t, d)
	defer stop()

	sendWithRetry(t, sock, "toggle") // Idle -> Recording
	sendWithRetry(t, sock, "toggle") // Recording -> Transcribing -> ... -> Idle
	waitForState(t, sock, "idle")

	want := []overlay.Visual{overlay.Recording, overlay.Transcribing, overlay.Hidden}
	got := ov.Calls()
	if len(got) != len(want) {
		t.Fatalf("overlay calls %v, want %v", got, want)
	}
	for i, v := range want {
		if got[i] != v {
			t.Fatalf("overlay.Calls[%d] = %v, want %v", i, got[i], v)
		}
	}
}

func TestTranscribeFailureReturnsToIdle(t *testing.T) {
	boom := errors.New("model on fire")
	d, sock := newTestDaemon(t, func(c *Config) {
		c.Transcriber = &speech.Mock{Err: boom}
	})
	stop := runDaemon(t, d)
	defer stop()

	sendWithRetry(t, sock, "toggle") // recording
	sendWithRetry(t, sock, "toggle") // transcribing -> failure -> idle
	waitForState(t, sock, "idle")
}

func TestEmptyTranscriptionSkipsOutput(t *testing.T) {
	out := &output.Mock{}
	d, sock := newTestDaemon(t, func(c *Config) {
		c.Transcriber = &speech.Mock{Text: ""}
		c.Output = out
	})
	stop := runDaemon(t, d)
	defer stop()

	sendWithRetry(t, sock, "toggle")
	sendWithRetry(t, sock, "toggle")
	waitForState(t, sock, "idle")

	if calls := out.Calls(); len(calls) != 0 {
		t.Fatalf("output should not be called for empty text, got %v", calls)
	}
}

func TestTranscribeFailureShowsErrorOverlay(t *testing.T) {
	boom := errors.New("model crash")
	ov := &overlay.Noop{}
	d, sock := newTestDaemon(t, func(c *Config) {
		c.Transcriber = &speech.Mock{Err: boom}
		c.Overlay = ov
	})
	stop := runDaemon(t, d)
	defer stop()

	sendWithRetry(t, sock, "toggle") // recording
	sendWithRetry(t, sock, "toggle") // transcribing -> failure (error overlay) -> idle
	waitForState(t, sock, "idle")

	calls := ov.Calls()
	hasError := false
	for _, v := range calls {
		if v == overlay.Error {
			hasError = true
			break
		}
	}
	if !hasError {
		t.Fatalf("expected overlay to show Error state, got calls: %v", calls)
	}
}

func TestDuckingOnRecordingAndRestoreOnIdle(t *testing.T) {
	ducker := &audio.MockDucker{}
	d, sock := newTestDaemon(t, func(c *Config) {
		c.Ducker = ducker
	})
	stop := runDaemon(t, d)
	defer stop()

	if ducker.IsDucked() {
		t.Fatalf("ducker should not be ducked initially")
	}

	// Toggle: Idle -> Recording
	sendWithRetry(t, sock, "toggle")
	waitForState(t, sock, "recording")

	if !ducker.IsDucked() {
		t.Fatalf("ducker should be ducked while in Recording")
	}
	duckCalls, _ := ducker.Calls()
	if duckCalls != 1 {
		t.Errorf("duckCalls = %d, want 1", duckCalls)
	}

	// Toggle: Recording -> Transcribing -> ... -> Idle
	sendWithRetry(t, sock, "toggle")
	waitForState(t, sock, "idle")

	if ducker.IsDucked() {
		t.Fatalf("ducker should be restored when returning to Idle")
	}
	// Restore fires on entering Transcribing (capture has stopped) and again
	// on Idle as a safety net for paths that skip Transcribing, so the count
	// is not pinned — what matters is one duck and an eventual restore.
	duckCalls, restoreCalls := ducker.Calls()
	if duckCalls != 1 || restoreCalls < 1 {
		t.Errorf("ducker calls = (%d, %d), want duck 1 and at least one restore", duckCalls, restoreCalls)
	}
}

func TestDuckingRestoresOnTranscribeFailure(t *testing.T) {
	ducker := &audio.MockDucker{}
	d, sock := newTestDaemon(t, func(c *Config) {
		c.Ducker = ducker
		c.Transcriber = &speech.Mock{Err: errors.New("transcribe failed")}
	})
	stop := runDaemon(t, d)
	defer stop()

	sendWithRetry(t, sock, "toggle")
	waitForState(t, sock, "recording")
	if !ducker.IsDucked() {
		t.Fatalf("ducker should be ducked in Recording")
	}

	sendWithRetry(t, sock, "toggle")
	waitForState(t, sock, "idle")
	if ducker.IsDucked() {
		t.Fatalf("ducker should be restored after transcribe failure")
	}
}

func TestLevelMonitoringDuringRecording(t *testing.T) {
	rec := &audio.MockRecorder{FixturePath: "/tmp/fake.wav", LevelVal: 0.65}
	ov := &overlay.Noop{}
	d, sock := newTestDaemon(t, func(c *Config) {
		c.Recorder = rec
		c.Overlay = ov
	})
	stop := runDaemon(t, d)
	defer stop()

	sendWithRetry(t, sock, "toggle") // -> Recording
	waitForState(t, sock, "recording")

	// Allow level monitor ticker to fire
	time.Sleep(100 * time.Millisecond)

	levels := ov.Levels()
	hasNonZero := false
	for _, l := range levels {
		if l > 0.5 {
			hasNonZero = true
			break
		}
	}
	if !hasNonZero {
		t.Fatalf("expected overlay to receive active audio level updates during recording, got: %v", levels)
	}

	sendWithRetry(t, sock, "toggle") // -> Transcribing -> Idle
	waitForState(t, sock, "idle")

	if last := ov.LastLevel(); last != 0.0 {
		t.Errorf("expected level to reset to 0.0 on idle, got %v", last)
	}
}

func TestStartAndStopTransitions(t *testing.T) {
	out := &output.Mock{}
	d, sock := newTestDaemon(t, func(c *Config) { c.Output = out })
	stop := runDaemon(t, d)
	defer stop()

	// start in Idle -> transitions to Recording
	resp := sendWithRetry(t, sock, "start")
	if resp.State != "recording" {
		t.Fatalf("start in idle returned state %q, want recording", resp.State)
	}
	waitForState(t, sock, "recording")

	// stop in Recording -> transitions to Transcribing
	resp = sendWithRetry(t, sock, "stop")
	if resp.State != "transcribing" {
		t.Fatalf("stop in recording returned state %q, want transcribing", resp.State)
	}

	waitForState(t, sock, "idle")

	calls := out.Calls()
	if len(calls) != 1 || calls[0] != "hello world" {
		t.Fatalf("output.Calls = %v, want [\"hello world\"]", calls)
	}
}

type blockingTranscriber struct {
	started chan struct{}
	release chan struct{}
}

func (b *blockingTranscriber) Transcribe(ctx context.Context, wavPath string) (string, error) {
	close(b.started)
	<-b.release
	return "hello world", nil
}

func TestStartIdempotency(t *testing.T) {
	out := &output.Mock{}
	started := make(chan struct{})
	release := make(chan struct{})
	d, sock := newTestDaemon(t, func(c *Config) {
		c.Output = out
		c.Transcriber = &blockingTranscriber{started: started, release: release}
	})
	stop := runDaemon(t, d)
	defer stop()

	// Initial start: Idle -> Recording
	resp := sendWithRetry(t, sock, "start")
	if resp.State != "recording" {
		t.Fatalf("first start returned state %q, want recording", resp.State)
	}
	waitForState(t, sock, "recording")

	// Repeated start in Recording: no-op, stays recording
	resp = sendWithRetry(t, sock, "start")
	if resp.State != "recording" {
		t.Fatalf("second start in recording returned state %q, want recording", resp.State)
	}
	if got := sendWithRetry(t, sock, "status"); got.State != "recording" {
		t.Fatalf("status after second start = %q, want recording", got.State)
	}

	// Transition to Transcribing
	resp = sendWithRetry(t, sock, "stop")
	if resp.State != "transcribing" {
		t.Fatalf("stop returned state %q, want transcribing", resp.State)
	}

	// Wait until Transcribe is actually executing in the background goroutine
	select {
	case <-started:
	case <-time.After(2 * time.Second):
		t.Fatal("transcription never started")
	}

	// Start while Transcribing: no-op, stays transcribing
	resp = sendWithRetry(t, sock, "start")
	if resp.State != "transcribing" {
		t.Fatalf("start while transcribing returned state %q, want transcribing", resp.State)
	}
	if got := sendWithRetry(t, sock, "status"); got.State != "transcribing" {
		t.Fatalf("status while transcribing = %q, want transcribing", got.State)
	}

	// Unblock transcription
	close(release)

	waitForState(t, sock, "idle")

	// Verify transcription completed normally and produced one output
	calls := out.Calls()
	if len(calls) != 1 || calls[0] != "hello world" {
		t.Fatalf("output.Calls = %v, want [\"hello world\"]", calls)
	}
}

func TestStopIdempotency(t *testing.T) {
	out := &output.Mock{}
	started := make(chan struct{})
	release := make(chan struct{})
	d, sock := newTestDaemon(t, func(c *Config) {
		c.Output = out
		c.Transcriber = &blockingTranscriber{started: started, release: release}
	})
	stop := runDaemon(t, d)
	defer stop()

	// Stop in Idle: no-op, stays idle
	resp := sendWithRetry(t, sock, "stop")
	if resp.State != "idle" {
		t.Fatalf("stop in idle returned state %q, want idle", resp.State)
	}
	if got := sendWithRetry(t, sock, "status"); got.State != "idle" {
		t.Fatalf("status after stop in idle = %q, want idle", got.State)
	}
	if calls := out.Calls(); len(calls) != 0 {
		t.Fatalf("unexpected output calls after stop in idle: %v", calls)
	}

	// Start -> Recording
	resp = sendWithRetry(t, sock, "start")
	if resp.State != "recording" {
		t.Fatalf("start returned state %q, want recording", resp.State)
	}
	waitForState(t, sock, "recording")

	// Stop -> Transcribing
	resp = sendWithRetry(t, sock, "stop")
	if resp.State != "transcribing" {
		t.Fatalf("stop in recording returned state %q, want transcribing", resp.State)
	}

	// Wait until Transcribe is executing
	select {
	case <-started:
	case <-time.After(2 * time.Second):
		t.Fatal("transcription never started")
	}

	// Stop while Transcribing: no-op, stays transcribing
	resp = sendWithRetry(t, sock, "stop")
	if resp.State != "transcribing" {
		t.Fatalf("stop in transcribing returned state %q, want transcribing", resp.State)
	}
	if got := sendWithRetry(t, sock, "status"); got.State != "transcribing" {
		t.Fatalf("status while transcribing = %q, want transcribing", got.State)
	}

	// Unblock transcription
	close(release)

	waitForState(t, sock, "idle")

	// Stop after returning to Idle: no-op, stays idle
	resp = sendWithRetry(t, sock, "stop")
	if resp.State != "idle" {
		t.Fatalf("stop after idle returned state %q, want idle", resp.State)
	}

	calls := out.Calls()
	if len(calls) != 1 || calls[0] != "hello world" {
		t.Fatalf("output.Calls = %v, want [\"hello world\"]", calls)
	}
}

func TestStreamingTokenUpdatesDuringRecording(t *testing.T) {
	streamTx := speech.NewMockStreamTranscriber("final transcript", "hel", "hello", "hello world")
	rec := &audio.MockRecorder{
		FixturePath: "/tmp/fake.wav",
		ChunkData:   []byte{1, 2, 3, 4},
	}
	ov := &overlay.Mock{}
	out := &output.Mock{}

	d, sock := newTestDaemon(t, func(c *Config) {
		c.Recorder = rec
		c.Transcriber = streamTx
		c.Overlay = ov
		c.Output = out
	})
	stop := runDaemon(t, d)
	defer stop()

	// Toggle: Idle -> Recording
	sendWithRetry(t, sock, "toggle")
	waitForState(t, sock, "recording")

	// Wait for streaming loop ticker to fire and feed chunks
	deadline := time.Now().Add(1 * time.Second)
	hasTokens := false
	for time.Now().Before(deadline) {
		texts := ov.Texts()
		for _, txt := range texts {
			if txt == "hel" || txt == "hello" || txt == "hello world" {
				hasTokens = true
				break
			}
		}
		if hasTokens {
			break
		}
		time.Sleep(20 * time.Millisecond)
	}

	if !hasTokens {
		t.Fatalf("overlay never received partial streaming tokens, overlay.Texts = %v", ov.Texts())
	}

	if chunks := streamTx.Chunks(); len(chunks) == 0 {
		t.Fatalf("streamTranscriber never received audio chunks")
	}

	// Toggle: Recording -> Transcribing -> Idle
	sendWithRetry(t, sock, "toggle")
	waitForState(t, sock, "idle")

	// Verify overlay text was cleared on completion
	if last := ov.LastText(); last != "" {
		t.Fatalf("expected overlay text to reset to \"\" on completion, got %q", last)
	}

	// Verify output dispatched the final transcript
	calls := out.Calls()
	if len(calls) != 1 || calls[0] != "final transcript" {
		t.Fatalf("output.Calls = %v, want [\"final transcript\"]", calls)
	}
}

func TestStreamingErrorResilience(t *testing.T) {
	streamTx := speech.NewMockStreamTranscriber("fallback transcript")
	streamTx.SetErrors(errors.New("stream start err"), errors.New("stream feed err"), errors.New("stream stop err"))
	rec := &audio.MockRecorder{
		FixturePath: "/tmp/fake.wav",
		ChunkData:   []byte{1, 2, 3, 4},
	}
	ov := &overlay.Mock{}
	out := &output.Mock{}

	d, sock := newTestDaemon(t, func(c *Config) {
		c.Recorder = rec
		c.Transcriber = streamTx
		c.Overlay = ov
		c.Output = out
	})
	stop := runDaemon(t, d)
	defer stop()

	// Should still cleanly transition to recording and back to idle despite streaming errors
	sendWithRetry(t, sock, "toggle")
	waitForState(t, sock, "recording")

	time.Sleep(80 * time.Millisecond)

	sendWithRetry(t, sock, "toggle")
	waitForState(t, sock, "idle")

	calls := out.Calls()
	if len(calls) != 1 || calls[0] != "fallback transcript" {
		t.Fatalf("output.Calls = %v, want [\"fallback transcript\"]", calls)
	}
}

// Streaming drives the overlay preview only. The final transcript is inserted
// once, when transcription completes — otherwise a streaming engine types
// every phrase as it is recognized and then the whole transcript again.
func TestStreamingDoesNotEmitOutput(t *testing.T) {
	out := &output.Mock{}
	d, sock := newTestDaemon(t, func(c *Config) {
		c.Output = out
		c.Transcriber = speech.NewMockStreamTranscriber("final transcript", "hel", "hello")
		c.Recorder = &audio.MockRecorder{FixturePath: "/tmp/fake.wav"}
		c.Mode = "streaming"
	})
	stop := runDaemon(t, d)
	defer stop()

	sendWithRetry(t, sock, "toggle")
	waitForState(t, sock, "recording")
	time.Sleep(120 * time.Millisecond) // let the stream loop run
	if got := out.Calls(); len(got) != 0 {
		t.Fatalf("output emitted %d times during Recording, want 0: %v", len(got), got)
	}

	sendWithRetry(t, sock, "toggle")
	waitForState(t, sock, "idle")

	got := out.Calls()
	if len(got) != 1 {
		t.Fatalf("output emitted %d times over the cycle, want exactly 1: %v", len(got), got)
	}
	if got[0] != "final transcript" {
		t.Errorf("emitted %q, want the final transcript", got[0])
	}
}

// mode = "batch" suppresses the live preview; the key is documented as
// meaningful and must actually gate something.
func TestBatchModeSuppressesStreamingPreview(t *testing.T) {
	ov := &overlay.Noop{}
	d, sock := newTestDaemon(t, func(c *Config) {
		c.Overlay = ov
		c.Transcriber = speech.NewMockStreamTranscriber("final transcript", "hel", "hello")
		c.Mode = "batch"
	})
	stop := runDaemon(t, d)
	defer stop()

	sendWithRetry(t, sock, "toggle")
	waitForState(t, sock, "recording")
	time.Sleep(120 * time.Millisecond)

	for _, txt := range ov.Texts() {
		if txt != "" {
			t.Fatalf("batch mode set preview text %q, want none", txt)
		}
	}
}

// Capture has already stopped by the time Transcribing begins, so media must
// come back up then rather than at the end of the tail.
func TestDuckingRestoresOnEnteringTranscribing(t *testing.T) {
	ducker := &audio.MockDucker{}
	d, sock := newTestDaemon(t, func(c *Config) {
		c.Ducker = ducker
		c.Transcriber = &speech.Mock{Text: "hello world", Delay: 300 * time.Millisecond}
	})
	stop := runDaemon(t, d)
	defer stop()

	sendWithRetry(t, sock, "toggle")
	waitForState(t, sock, "recording")
	if !ducker.IsDucked() {
		t.Fatalf("ducker should be ducked while Recording")
	}

	sendWithRetry(t, sock, "toggle")
	waitForState(t, sock, "transcribing")
	if ducker.IsDucked() {
		t.Fatalf("ducker should be restored on entering Transcribing, not held through the tail")
	}
}

// Every completed transcript is recorded before it is typed, so text that never
// reaches the focused window can still be recovered.
func TestTranscriptIsRecordedToHistory(t *testing.T) {
	dir := t.TempDir()
	store := &history.Store{Path: filepath.Join(dir, "history.jsonl")}
	d, sock := newTestDaemon(t, func(c *Config) {
		c.History = store
		c.Transcriber = &speech.Mock{Text: "recover me"}
	})
	stop := runDaemon(t, d)
	defer stop()

	sendWithRetry(t, sock, "toggle")
	waitForState(t, sock, "recording")
	sendWithRetry(t, sock, "toggle")
	waitForState(t, sock, "idle")

	got, err := store.Recent(0)
	if err != nil {
		t.Fatalf("Recent: %v", err)
	}
	if len(got) != 1 || got[0].Text != "recover me" {
		t.Fatalf("history = %v, want one entry %q", got, "recover me")
	}
}

// Output failure is exactly when recovery matters, so the record must already
// be on disk by then.
func TestTranscriptRecordedEvenWhenOutputFails(t *testing.T) {
	dir := t.TempDir()
	store := &history.Store{Path: filepath.Join(dir, "history.jsonl")}
	d, sock := newTestDaemon(t, func(c *Config) {
		c.History = store
		c.Output = &output.Mock{Err: errors.New("no window focused")}
		c.Transcriber = &speech.Mock{Text: "lost text"}
	})
	stop := runDaemon(t, d)
	defer stop()

	sendWithRetry(t, sock, "toggle")
	waitForState(t, sock, "recording")
	sendWithRetry(t, sock, "toggle")
	waitForState(t, sock, "idle")

	got, err := store.Recent(0)
	if err != nil {
		t.Fatalf("Recent: %v", err)
	}
	if len(got) != 1 || got[0].Text != "lost text" {
		t.Fatalf("history = %v, want the transcript recorded despite output failure", got)
	}
}
