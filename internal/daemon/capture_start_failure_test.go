package daemon

import (
	"context"
	"errors"
	"sync"
	"testing"

	"github.com/mschulkind-oss/mavor/internal/audio"
	"github.com/mschulkind-oss/mavor/internal/output"
	"github.com/mschulkind-oss/mavor/internal/overlay"
	"github.com/mschulkind-oss/mavor/internal/speech"
)

// failStartRecorder fails the Nth Start (1-based) with err and delegates every
// other call to the wrapped recorder. It exists because audio.MockRecorder
// cannot inject a Start error, which left the capture-start-failure path — the
// design's risk #2 and a documented past real bug — with no daemon-level test.
type failStartRecorder struct {
	audio.Recorder
	mu     sync.Mutex
	failAt int
	starts int
	err    error
}

func (r *failStartRecorder) Start(ctx context.Context) error {
	r.mu.Lock()
	r.starts++
	n := r.starts
	r.mu.Unlock()
	if r.failAt != 0 && n == r.failAt {
		return r.err
	}
	return r.Recorder.Start(ctx)
}

// A recorder that will not start drives abortCycle: the failure must undo its
// duck, paint the error, retire the cycle so a later dictation is not stuck
// behind its id, and leave the daemon able to record again.
func TestCaptureStartFailureRestoresDuckingAndAdvancesTheQueue(t *testing.T) {
	boom := errors.New("parec: no audio server")
	rec := &failStartRecorder{
		Recorder: &audio.MockRecorder{FixturePath: "/tmp/capture.wav"},
		failAt:   1,
		err:      boom,
	}
	ducker := &audio.MockDucker{}
	out := &output.Mock{}
	ov := &overlay.Mock{}
	d, sock := newTestDaemon(t, func(c *Config) {
		c.Recorder = rec
		c.Ducker = ducker
		c.Output = out
		c.Overlay = ov
		c.Transcriber = &speech.Mock{Text: "recovered"}
	})
	stop := runDaemon(t, d)
	defer stop()

	resp := sendWithRetry(t, sock, "start")
	if resp.State != "idle" {
		t.Fatalf("failed start returned %q, want idle", resp.State)
	}
	if resp.Recording {
		t.Fatalf("failed start still reports a recording: %+v", resp)
	}
	if ducker.IsDucked() {
		t.Fatal("aborted capture left background audio ducked")
	}
	if duck, restore := ducker.Calls(); duck != 1 || restore != 1 {
		t.Fatalf("ducker calls = (%d, %d), want one duck and one restore", duck, restore)
	}
	if !textSeen(ov.Texts(), boom.Error()) {
		t.Fatalf("capture-start error never painted, texts = %v", ov.Texts())
	}
	// The aborted capture must also clear the single-capture field: abortCycle
	// is the only writer of it other than start/stop, so leaving it set carries
	// a second source of truth that can disagree with the machine's slot.
	if d.recording != nil {
		t.Fatalf("abortCycle left d.recording set: %+v", d.recording)
	}

	// The aborted cycle has to complete: a later dictation types only if the
	// dispatcher's frontier moved past the aborted id rather than parking every
	// later cycle behind it forever.
	if resp := sendWithRetry(t, sock, "start"); resp.State != "recording" {
		t.Fatalf("start after abort returned %q, want recording", resp.State)
	}
	if !ducker.IsDucked() {
		t.Fatal("second recording did not duck")
	}
	sendWithRetry(t, sock, "stop")
	waitForState(t, sock, "idle")
	if calls := out.Calls(); len(calls) != 1 || calls[0] != "recovered" {
		t.Fatalf("output = %v, want [\"recovered\"]", calls)
	}
}
