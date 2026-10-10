package daemon

import (
	"context"
	"fmt"
	"path/filepath"
	"sync"
	"testing"
	"time"

	"github.com/mschulkind-oss/mavor/internal/history"
	"github.com/mschulkind-oss/mavor/internal/speech"
)

// gatedEmitter is an output.Dispatcher a test can hold an emit in flight with.
// output.Mock records calls but cannot block, so without this the daemon's two
// ordering claims have no test that can see an emit that has not returned:
// "Idle means typed" (a cycle is not retired until its output is out) and
// "spoken order beats completion order" (a later cycle waits for its turn).
//
// Emit signals arrival and blocks the FIRST call until unblock is called;
// later calls record and return, so a second dictation's emit can be observed
// arriving while the first is still held.
type gatedEmitter struct {
	mu       sync.Mutex
	started  []string // every Emit entry, in invocation order
	finished []string // every Emit return, in completion order
	arrived  chan string

	first       sync.Once
	entered     chan struct{} // closed when the first Emit is entered
	release     chan struct{} // closed by unblock to let the first Emit return
	releaseOnce sync.Once
}

func newGatedEmitter() *gatedEmitter {
	return &gatedEmitter{
		arrived: make(chan string, 8),
		entered: make(chan struct{}),
		release: make(chan struct{}),
	}
}

func (g *gatedEmitter) Emit(_ context.Context, text string) error {
	g.mu.Lock()
	g.started = append(g.started, text)
	g.mu.Unlock()
	g.arrived <- text
	g.first.Do(func() {
		close(g.entered)
		<-g.release
	})
	g.mu.Lock()
	g.finished = append(g.finished, text)
	g.mu.Unlock()
	return nil
}

// unblock releases the first Emit, idempotently, so a test that fails early
// still lets the daemon shut down rather than hanging its goroutine join.
func (g *gatedEmitter) unblock() {
	g.releaseOnce.Do(func() { close(g.release) })
}

func (g *gatedEmitter) startedCalls() []string {
	g.mu.Lock()
	defer g.mu.Unlock()
	return append([]string(nil), g.started...)
}

func (g *gatedEmitter) finishedCalls() []string {
	g.mu.Lock()
	defer g.mu.Unlock()
	return append([]string(nil), g.finished...)
}

// signaledTranscriber returns one configured text per decode and signals each
// completed decode, so a test can wait for a second dictation's decode to
// finish while the first dictation's emit is still blocked.
type signaledTranscriber struct {
	mu      sync.Mutex
	texts   []string
	idx     int
	decoded chan string
}

func (s *signaledTranscriber) Transcribe(_ context.Context, _ string) (string, error) {
	s.mu.Lock()
	i := s.idx
	s.idx++
	text := fmt.Sprintf("dictation %d", i+1)
	if i < len(s.texts) {
		text = s.texts[i]
	}
	s.mu.Unlock()
	if s.decoded != nil {
		s.decoded <- text
	}
	return text, nil
}

// overtakeWindow is how long a completion-order dispatcher is given to type a
// finished later cycle out of turn before the test concludes it is waiting.
const overtakeWindow = 100 * time.Millisecond

// Claim 1 — Idle means typed. While an Emit is in flight the daemon's own IPC
// status surface must still report transcribing, because the cycle is not
// retired from the machine until its output is out; it becomes idle only once
// Emit returns. This is what docs/reference/how-mavor-works.md says Idle means.
func TestIdleOnlyAfterEmitReturns(t *testing.T) {
	gate := newGatedEmitter()
	store := &history.Store{Path: filepath.Join(t.TempDir(), "history.jsonl")}
	d, sock := newTestDaemon(t, func(c *Config) {
		c.Output = gate
		c.History = store
		c.Transcriber = &speech.Mock{Text: "the typed thought"}
		c.PreviewEnabled = false
	})
	stop := runDaemon(t, d)
	defer stop()
	// Registered after stop so it runs first: a failing test releases the held
	// emit before cancelling the daemon, rather than timing out its goroutine join.
	defer gate.unblock()

	sendWithRetry(t, sock, "start")
	waitForState(t, sock, "recording")
	sendWithRetry(t, sock, "stop")

	// The decode finished; Emit is now blocked in flight.
	select {
	case <-gate.entered:
	case <-time.After(sendTimeout):
		t.Fatal("claim 1 (idle-means-typed): Emit never entered")
	}

	// The daemon's own surface must still say transcribing while the output
	// is out of its hands.
	got := sendWithRetry(t, sock, "status")
	if got.State != "transcribing" || got.Transcribing != 1 {
		t.Fatalf("claim 1 (idle-means-typed): status while Emit is in flight = %+v, want transcribing with one pending cycle", got)
	}

	gate.unblock()
	waitForState(t, sock, "idle")

	if calls := gate.startedCalls(); len(calls) != 1 || calls[0] != "the typed thought" {
		t.Fatalf("claim 1 (idle-means-typed): emitted %v, want the single thought", calls)
	}
}

// Claim 2 — Spoken order beats completion order at the daemon level. The first
// dictation's emit is held in flight; the second dictation's decode finishes
// behind it. The second must wait its turn rather than overtake: no second Emit
// may start, and the finished order and the history order must both be the
// spoken order. A dispatcher that emits each finished cycle on completion
// instead of waiting for its predecessor fails the overtake check; one that
// emits on the decode worker fails to reach the second decode at all.
func TestSecondDictationWaitsForTheFirstEmit(t *testing.T) {
	gate := newGatedEmitter()
	store := &history.Store{Path: filepath.Join(t.TempDir(), "history.jsonl")}
	tr := &signaledTranscriber{
		texts:   []string{"the first thought", "the second thought"},
		decoded: make(chan string, 2),
	}
	d, sock := newTestDaemon(t, func(c *Config) {
		c.Output = gate
		c.History = store
		c.Transcriber = tr
		c.PreviewEnabled = false
	})
	stop := runDaemon(t, d)
	defer stop()
	// Registered after stop so it runs first, keeping a failure's shutdown clean.
	defer gate.unblock()

	// Two dictations back to back, both queued for decode in spoken order.
	sendWithRetry(t, sock, "start")
	sendWithRetry(t, sock, "stop")
	sendWithRetry(t, sock, "start")
	sendWithRetry(t, sock, "stop")

	// The first decode finishes and its Emit is held in flight.
	if got := nextDecode(t, tr); got != "the first thought" {
		t.Fatalf("claim 2 (spoken order beats completion order): first decode returned %q", got)
	}
	select {
	case <-gate.entered:
	case <-time.After(sendTimeout):
		t.Fatal("claim 2 (spoken order beats completion order): the first Emit never entered")
	}
	if got := <-gate.arrived; got != "the first thought" {
		t.Fatalf("claim 2 (spoken order beats completion order): first Emit carried %q", got)
	}

	// The second dictation's decode completes while the first Emit is blocked.
	if got := nextDecode(t, tr); got != "the second thought" {
		t.Fatalf("claim 2 (spoken order beats completion order): second decode returned %q", got)
	}

	// It must not overtake: its Emit may not start while the first is in flight.
	select {
	case got := <-gate.arrived:
		t.Fatalf("claim 2 (spoken order beats completion order): %q was emitted while the first dictation's Emit was still in flight — the second overtook the first", got)
	case <-time.After(overtakeWindow):
	}

	gate.unblock()
	waitForState(t, sock, "idle")

	if got := gate.finishedCalls(); len(got) != 2 || got[0] != "the first thought" || got[1] != "the second thought" {
		t.Fatalf("claim 2 (spoken order beats completion order): Emit completion order = %v, want [the first thought, the second thought]", got)
	}
	entries, err := store.Recent(0)
	if err != nil {
		t.Fatal(err)
	}
	if len(entries) != 2 || entries[0].Text != "the second thought" || entries[1].Text != "the first thought" {
		t.Fatalf("claim 2 (spoken order beats completion order): history newest-first = %+v, want the second then the first", entries)
	}
}

// nextDecode reads one completed decode, failing if none arrives in time.
func nextDecode(t *testing.T, tr *signaledTranscriber) string {
	t.Helper()
	select {
	case got := <-tr.decoded:
		return got
	case <-time.After(sendTimeout):
		t.Fatal("claim 2 (spoken order beats completion order): a dictation never finished decoding")
		return ""
	}
}
