package daemon

import (
	"context"
	"errors"
	"fmt"
	"path/filepath"
	"sync"
	"testing"
	"time"

	"github.com/mschulkind-oss/mavor/internal/audio"
	"github.com/mschulkind-oss/mavor/internal/history"
	"github.com/mschulkind-oss/mavor/internal/ipc"
	"github.com/mschulkind-oss/mavor/internal/models"
	"github.com/mschulkind-oss/mavor/internal/output"
	"github.com/mschulkind-oss/mavor/internal/overlay"
	"github.com/mschulkind-oss/mavor/internal/speech"
	"github.com/mschulkind-oss/mavor/internal/state"
)

// sequencedTranscriber returns one configured text per call, so a stacked
// dictation's output can be told apart from the one before it. If hold is set,
// the first call blocks until it is closed.
type sequencedTranscriber struct {
	mu    sync.Mutex
	texts []string
	idx   int
	first chan struct{}
	hold  chan struct{}
	once  sync.Once
}

func (s *sequencedTranscriber) Transcribe(_ context.Context, _ string) (string, error) {
	s.mu.Lock()
	i := s.idx
	s.idx++
	text := fmt.Sprintf("dictation %d", i+1)
	if i < len(s.texts) {
		text = s.texts[i]
	}
	s.mu.Unlock()
	if s.hold != nil {
		s.once.Do(func() {
			if s.first != nil {
				close(s.first)
			}
			<-s.hold
		})
	}
	return text, nil
}

// Two dictations back to back: the second records while the first still
// decodes, both finish, and the text comes out in spoken order — never
// completion order — with history matching what was typed.
func TestStackedDictationsEmitInSpokenOrderAndReturnToIdleOnTheLast(t *testing.T) {
	out := &output.Mock{}
	store := &history.Store{Path: filepath.Join(t.TempDir(), "history.jsonl")}
	tr := &sequencedTranscriber{
		texts: []string{"the first thought", "the second thought"},
		first: make(chan struct{}),
		hold:  make(chan struct{}),
	}
	d, sock := newTestDaemon(t, func(c *Config) {
		c.Output = out
		c.History = store
		c.Transcriber = tr
	})
	stop := runDaemon(t, d)
	defer stop()

	sendWithRetry(t, sock, "start")
	waitForState(t, sock, "recording")

	// Stop the first; the decoder blocks on the held first transcription.
	sendWithRetry(t, sock, "stop")
	waitForState(t, sock, "transcribing")
	select {
	case <-tr.first:
	case <-time.After(sendTimeout):
		t.Fatal("first transcription never started")
	}

	// Start the second while the first still decodes. The stack exists: a
	// recording over a pending cycle reads as recording, with the earlier one
	// still counted as transcribing.
	sendWithRetry(t, sock, "start")
	waitForState(t, sock, "recording")
	if got := sendWithRetry(t, sock, "status"); !got.Recording || got.Transcribing != 1 {
		t.Fatalf("status over one pending cycle = %+v, want recording with transcribing=1", got)
	}

	// Stop the second: two pending, still transcribing, not idle yet — Idle
	// returns only when the last one finishes.
	sendWithRetry(t, sock, "stop")
	got := sendWithRetry(t, sock, "status")
	if got.State != "transcribing" || got.Transcribing != 2 || got.Recording {
		t.Fatalf("status with two pending = %+v, want transcribing with count 2", got)
	}
	if got.State == "idle" {
		t.Fatal("returned to idle before the last dictation finished")
	}

	close(tr.hold)
	waitForState(t, sock, "idle")

	if calls := out.Calls(); len(calls) != 2 || calls[0] != "the first thought" || calls[1] != "the second thought" {
		t.Fatalf("output = %v, want the two thoughts in spoken order", calls)
	}
	entries, err := store.Recent(0)
	if err != nil {
		t.Fatal(err)
	}
	if len(entries) != 2 || entries[0].Text != "the second thought" || entries[1].Text != "the first thought" {
		t.Fatalf("history newest-first = %+v, want the second then the first", entries)
	}
}

// The pending queue is bounded: a further recording is refused at the cap
// rather than growing disk use without limit.
func TestPendingCapRefusesAnotherRecording(t *testing.T) {
	tr := &sequencedTranscriber{
		first: make(chan struct{}),
		hold:  make(chan struct{}),
	}
	d, sock := newTestDaemon(t, func(c *Config) {
		c.Transcriber = tr
		c.Output = &output.Mock{}
	})
	stop := runDaemon(t, d)
	defer stop()

	// Fill the queue. The first decode is held, so every cycle stays pending.
	for i := 0; i < state.MaxPending; i++ {
		sendWithRetry(t, sock, "start")
		sendWithRetry(t, sock, "stop")
	}
	status := sendWithRetry(t, sock, "status")
	if status.Transcribing != state.MaxPending || status.Recording {
		t.Fatalf("status at the cap = %+v, want transcribing=%d", status, state.MaxPending)
	}

	// A start at the cap is refused: still transcribing, no new recording.
	resp := sendWithRetry(t, sock, "start")
	if resp.State != "transcribing" || resp.Recording {
		t.Fatalf("start at the cap = %+v, want refused (transcribing)", resp)
	}

	// Draining the queue lets recordings start again.
	close(tr.hold)
	waitForState(t, sock, "idle")
	if resp := sendWithRetry(t, sock, "start"); resp.State != "recording" {
		t.Fatalf("start after draining = %q, want recording", resp.State)
	}
}

// Live-final modes do not stack (OQ-3): a start while one is transcribing is
// today's no-op, so the one recognizer is never asked for two streams.
func TestLiveFinalModeDoesNotStack(t *testing.T) {
	wav := filepath.Join(t.TempDir(), "capture.wav")
	if err := audio.WriteWAV(wav, []int16{1, 2, 3}, 16000); err != nil {
		t.Fatal(err)
	}
	out := &output.Mock{}
	main := &heldFinalStop{speech.NewMockStreamTranscriber("main final"), make(chan struct{}), make(chan struct{})}
	d, sock := newTestDaemon(t, func(c *Config) {
		c.FinalMode = models.FinalStreaming
		c.Recorder = &audio.MockRecorder{FixturePath: wav}
		c.Output = out
		c.Transcriber = main
		c.PreviewEnabled = false
	})
	stop := runDaemon(t, d)
	defer stop()

	sendWithRetry(t, sock, "start")
	sendWithRetry(t, sock, "stop")
	select {
	case <-main.entered:
	case <-time.After(sendTimeout):
		t.Fatal("final decode never started")
	}

	resp := sendWithRetry(t, sock, "start")
	if resp.State != "transcribing" {
		t.Fatalf("start while transcribing in a live-final mode = %q, want transcribing (no stacking)", resp.State)
	}
	close(main.release)
	waitForState(t, sock, "idle")
}

// The CLI reads the stack from the response, not by parsing the state word.
func TestIPCResponseCarriesTheStack(t *testing.T) {
	tr := &sequencedTranscriber{first: make(chan struct{}), hold: make(chan struct{})}
	d, sock := newTestDaemon(t, func(c *Config) {
		c.Transcriber = tr
		c.Output = &output.Mock{}
	})
	stop := runDaemon(t, d)
	defer stop()

	var resp ipc.Response
	sendWithRetry(t, sock, "start")
	sendWithRetry(t, sock, "stop")
	resp = sendWithRetry(t, sock, "status")
	if resp.Recording || resp.Transcribing != 1 {
		t.Fatalf("status = %+v, want transcribing=1 and no recording", resp)
	}
	sendWithRetry(t, sock, "start")
	resp = sendWithRetry(t, sock, "status")
	if !resp.Recording || resp.Transcribing != 1 {
		t.Fatalf("status = %+v, want recording with transcribing=1", resp)
	}
	sendWithRetry(t, sock, "stop")
	close(tr.hold)
	waitForState(t, sock, "idle")
}

// failFirstTranscriber errors on its first call and succeeds afterwards, so a
// stacked failure can be observed without wedging the cycle behind it. If hold
// is set the first call blocks until it is closed.
type failFirstTranscriber struct {
	mu    sync.Mutex
	calls int
	first chan struct{}
	hold  chan struct{}
	once  sync.Once
}

func (f *failFirstTranscriber) Transcribe(_ context.Context, _ string) (string, error) {
	f.mu.Lock()
	f.calls++
	n := f.calls
	f.mu.Unlock()
	if n == 1 {
		if f.hold != nil {
			f.once.Do(func() {
				close(f.first)
				<-f.hold
			})
		}
		return "", errors.New("first dictation blew up")
	}
	return "second survived", nil
}

// A failure is per-cycle: it shows its error, advances spoken order, and does
// not stop a later stacked dictation from being typed.
func TestStackedFailureDoesNotWedgeTheNextDictation(t *testing.T) {
	out := &output.Mock{}
	tr := &failFirstTranscriber{first: make(chan struct{}), hold: make(chan struct{})}
	d, sock := newTestDaemon(t, func(c *Config) {
		c.Output = out
		c.Transcriber = tr
	})
	stop := runDaemon(t, d)
	defer stop()

	sendWithRetry(t, sock, "start")
	sendWithRetry(t, sock, "stop")
	select {
	case <-tr.first:
	case <-time.After(sendTimeout):
		t.Fatal("first transcription never started")
	}

	// Stack a second dictation while the first is still failing.
	sendWithRetry(t, sock, "start")
	sendWithRetry(t, sock, "stop")
	close(tr.hold)

	waitForState(t, sock, "idle")
	if calls := out.Calls(); len(calls) != 1 || calls[0] != "second survived" {
		t.Fatalf("output = %v, want only the second dictation's text", calls)
	}
}

// stackSeen reports whether a recorded overlay call sequence asked for the
// given pill stack.
func stackSeen(stacks [][]overlay.Visual, want []overlay.Visual) bool {
	for _, s := range stacks {
		if len(s) != len(want) {
			continue
		}
		match := true
		for i := range want {
			if s[i] != want[i] {
				match = false
				break
			}
		}
		if match {
			return true
		}
	}
	return false
}

// textSeen reports whether a recorded overlay call sequence carried the given
// subtitle.
func textSeen(texts []string, want string) bool {
	for _, s := range texts {
		if s == want {
			return true
		}
	}
	return false
}

// Recording over a transcribing cycle asks the overlay for two pills: the live
// recording row above the earlier dictation still awaiting output.
func TestStackedDictationsRequestTwoPills(t *testing.T) {
	ov := &overlay.Mock{}
	tr := &sequencedTranscriber{
		texts: []string{"the first thought", "the second thought"},
		first: make(chan struct{}),
		hold:  make(chan struct{}),
	}
	d, sock := newTestDaemon(t, func(c *Config) {
		c.Overlay = ov
		c.Transcriber = tr
	})
	stop := runDaemon(t, d)
	defer stop()

	sendWithRetry(t, sock, "start")
	waitForState(t, sock, "recording")
	sendWithRetry(t, sock, "stop")
	waitForState(t, sock, "transcribing")
	select {
	case <-tr.first:
	case <-time.After(sendTimeout):
		t.Fatal("first transcription never started")
	}

	// Record the second while the first still decodes.
	sendWithRetry(t, sock, "start")
	waitForState(t, sock, "recording")

	if !stackSeen(ov.Stacks(), []overlay.Visual{overlay.Recording, overlay.Transcribing}) {
		t.Fatalf("no [Recording Transcribing] stack among %v", ov.Stacks())
	}

	sendWithRetry(t, sock, "stop")
	close(tr.hold)
	waitForState(t, sock, "idle")
}

// A single dictation keeps its one-pill rendering: every requested stack is a
// lone row, and the primaries still move Recording -> Transcribing -> Hidden.
func TestSingleDictationRequestsOnePill(t *testing.T) {
	ov := &overlay.Mock{}
	d, sock := newTestDaemon(t, func(c *Config) {
		c.Overlay = ov
		c.Transcriber = &speech.Mock{Text: "one thought"}
	})
	stop := runDaemon(t, d)
	defer stop()

	sendWithRetry(t, sock, "start")
	waitForState(t, sock, "recording")
	sendWithRetry(t, sock, "stop")
	waitForState(t, sock, "idle")

	stacks := ov.Stacks()
	if len(stacks) == 0 {
		t.Fatal("no overlay stacks requested")
	}
	for _, s := range stacks {
		if len(s) != 1 {
			t.Fatalf("single dictation requested a %d-pill stack: %v", len(s), s)
		}
	}
	want := []overlay.Visual{overlay.Recording, overlay.Transcribing, overlay.Hidden}
	if got := ov.Calls(); len(got) != len(want) {
		t.Fatalf("overlay primaries = %v, want %v", got, want)
	} else {
		for i := range want {
			if got[i] != want[i] {
				t.Fatalf("overlay primary %d = %v, want %v", i, got[i], want[i])
			}
		}
	}
}

// §7.6: a failed cycle's error pill joins the live stack instead of replacing
// it, so a recording row and its error are requested together.
func TestStackedFailureRendersErrorAlongsideTheRecording(t *testing.T) {
	ov := &overlay.Mock{}
	tr := &failFirstTranscriber{first: make(chan struct{}), hold: make(chan struct{})}
	d, sock := newTestDaemon(t, func(c *Config) {
		c.Overlay = ov
		c.Transcriber = tr
	})
	stop := runDaemon(t, d)
	defer stop()

	sendWithRetry(t, sock, "start")
	sendWithRetry(t, sock, "stop")
	select {
	case <-tr.first:
	case <-time.After(sendTimeout):
		t.Fatal("first transcription never started")
	}

	// A second recording starts while the first is still failing.
	sendWithRetry(t, sock, "start")
	waitForState(t, sock, "recording")
	close(tr.hold)

	deadline := time.Now().Add(sendTimeout)
	for time.Now().Before(deadline) {
		if stackSeen(ov.Stacks(), []overlay.Visual{overlay.Recording, overlay.Error}) {
			break
		}
		time.Sleep(10 * time.Millisecond)
	}
	if !stackSeen(ov.Stacks(), []overlay.Visual{overlay.Recording, overlay.Error}) {
		t.Fatalf("no [Recording Error] stack among %v", ov.Stacks())
	}

	sendWithRetry(t, sock, "stop")
	waitForState(t, sock, "idle")

	// The error message was painted with the error row, then cleared once the
	// row left the stack.
	if !textSeen(ov.Texts(), "first dictation blew up") {
		t.Fatalf("error subtitle never painted, texts = %v", ov.Texts())
	}
	if got := ov.LastText(); got != "" {
		t.Fatalf("diagnostic subtitle lingered after the error row left the stack: %q", got)
	}
}
