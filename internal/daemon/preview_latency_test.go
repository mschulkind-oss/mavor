package daemon

import (
	"context"
	"sync"
	"testing"
	"time"

	"github.com/mschulkind-oss/mavor/internal/audio"
	"github.com/mschulkind-oss/mavor/internal/ipc"
	"github.com/mschulkind-oss/mavor/internal/output"
	"github.com/mschulkind-oss/mavor/internal/overlay"
	"github.com/mschulkind-oss/mavor/internal/speech"
)

// slowStopCompanion wraps a MockStreamTranscriber whose StopStream takes a
// configurable amount of time to return, to stand in for a real recognizer's
// final decode of the tail of the utterance. Its result is thrown away by
// stopStreamingMonitoring (the preview never contributes to the transcript),
// so this delay buys nothing — it is pure added latency if anything on the
// user-facing critical path waits for it.
type slowStopCompanion struct {
	*speech.MockStreamTranscriber
	delay time.Duration

	mu       sync.Mutex
	stopping bool
	overlap  bool // a StartStream arrived while StopStream was still running
}

func (s *slowStopCompanion) StopStream(ctx context.Context) (string, error) {
	s.mu.Lock()
	s.stopping = true
	s.mu.Unlock()

	time.Sleep(s.delay)

	s.mu.Lock()
	s.stopping = false
	s.mu.Unlock()
	return s.MockStreamTranscriber.StopStream(ctx)
}

func (s *slowStopCompanion) StartStream(ctx context.Context) error {
	s.mu.Lock()
	if s.stopping {
		s.overlap = true
	}
	s.mu.Unlock()
	return s.MockStreamTranscriber.StartStream(ctx)
}

func (s *slowStopCompanion) overlapped() bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.overlap
}

// timedTranscriber records the wall-clock time Transcribe was actually
// invoked, so a test can measure how long the pipeline took to reach the
// real, output-producing decode after the user asked to stop recording.
type timedTranscriber struct {
	speech.Mock
	called chan time.Time
}

func (t *timedTranscriber) Transcribe(ctx context.Context, wavPath string) (string, error) {
	select {
	case t.called <- time.Now():
	default:
	}
	return t.Mock.Transcribe(ctx, wavPath)
}

// The preview's final decode is thrown away — the transcript always comes
// from the main model's single Transcribe — so nothing a user waits for may
// wait for it.
//
// It used to. stopStreamingMonitoring called StopStream synchronously, on the
// FSM listener that state.Machine.Apply runs BEFORE returning, so a slow
// companion decode of the tail was added twice over: once to the reply to the
// user's own `mavor stop` / `toggle`, and again to the delay before
// runTranscription — the call producing the text they are actually waiting
// for — was even spawned. With a 150ms decode both were 150ms; they are now
// well under a millisecond.
func TestTheDiscardedPreviewDecodeGatesNothing(t *testing.T) {
	const (
		stopDelay = 150 * time.Millisecond
		// Generous against a loaded CI box while still an order of magnitude
		// under the delay: the assertion is "does not wait for it", and any
		// value that no longer tracks stopDelay proves that.
		ceiling = 50 * time.Millisecond
	)

	companion := &slowStopCompanion{
		MockStreamTranscriber: speech.NewMockStreamTranscriber("discarded final text"),
		delay:                 stopDelay,
	}
	tr := &timedTranscriber{
		Mock:   speech.Mock{Text: "the real transcript"},
		called: make(chan time.Time, 1),
	}

	d, sock := newTestDaemon(t, func(c *Config) {
		c.Recorder = &audio.MockRecorder{FixturePath: "/tmp/fake.wav", ChunkData: []byte{1, 2, 3, 4}}
		c.Transcriber = tr
		c.Output = &output.Mock{}
		c.Overlay = &overlay.Noop{}
		c.PreviewMode = speech.PreviewCompanion
		c.PreviewCompanion = companion
	})
	stop := runDaemon(t, d)
	defer stop()

	sendWithRetry(t, sock, "toggle")
	waitForState(t, sock, "recording")

	beforeStop := time.Now()
	resp, err := ipc.Send(sock, ipc.Request{Action: "toggle"}, 2*time.Second)
	ipcRespondedAt := time.Now()
	if err != nil {
		t.Fatalf("toggle: %v", err)
	}
	if resp.State != "transcribing" {
		t.Fatalf("state after stop = %q, want transcribing", resp.State)
	}

	select {
	case transcribeAt := <-tr.called:
		waited := transcribeAt.Sub(beforeStop)
		t.Logf("Transcribe started %v after stop (companion decode takes %v)", waited, stopDelay)
		if waited > ceiling {
			t.Errorf("Transcribe started %v after stop was requested, over the %v ceiling: "+
				"the main model is waiting on a preview decode whose result is discarded", waited, ceiling)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("Transcribe was never called")
	}

	waited := ipcRespondedAt.Sub(beforeStop)
	t.Logf("ipc toggle replied in %v", waited)
	if waited > ceiling {
		t.Errorf("`mavor toggle` took %v to reply, over the %v ceiling: "+
			"the command is waiting on a preview decode whose result is discarded", waited, ceiling)
	}
}

// The cost of moving that decode off the critical path is that it is now
// concurrent with whatever happens next — and what happens next may be the
// user starting to speak again, which restarts the SAME recognizer object.
// Feeding a new stream to a recognizer still finishing the last one is the
// bug this fix could have traded for, so the next StartStream waits for the
// drain.
func TestTheNextRecordingWaitsForTheDrain(t *testing.T) {
	companion := &slowStopCompanion{
		MockStreamTranscriber: speech.NewMockStreamTranscriber("discarded"),
		delay:                 120 * time.Millisecond,
	}

	d, sock := newTestDaemon(t, func(c *Config) {
		c.Recorder = &audio.MockRecorder{FixturePath: "/tmp/fake.wav", ChunkData: []byte{1, 2, 3, 4}}
		c.Transcriber = &speech.Mock{Text: "text"}
		c.Output = &output.Mock{}
		c.Overlay = &overlay.Noop{}
		c.PreviewMode = speech.PreviewCompanion
		c.PreviewCompanion = companion
	})
	stop := runDaemon(t, d)
	defer stop()

	// Record, stop, and immediately record again — the drain from the first
	// stop is still running when the second recording starts.
	for i := 0; i < 3; i++ {
		sendWithRetry(t, sock, "toggle")
		waitForState(t, sock, "recording")
		if _, err := ipc.Send(sock, ipc.Request{Action: "toggle"}, 2*time.Second); err != nil {
			t.Fatalf("stop %d: %v", i, err)
		}
	}

	if companion.overlapped() {
		t.Error("StartStream was called while StopStream was still running: " +
			"the recognizer is being fed a new stream before it finished the last")
	}
}
