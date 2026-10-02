package speech

import (
	"bytes"
	"context"
	"encoding/binary"
	"errors"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"sync"
	"testing"

	"github.com/mschulkind-oss/mavor/internal/audio"
	"github.com/mschulkind-oss/mavor/internal/models"
)

type finalFake struct {
	mu                         sync.Mutex
	pcm                        []byte
	replays, stops, aborts     int
	startErr, feedErr, stopErr error
	entered, release           chan struct{}
	final                      string
	replayPath                 string
}

func (f *finalFake) StartStream(context.Context) error { return f.startErr }
func (f *finalFake) FeedChunk(ctx context.Context, b []byte) (string, error) {
	if f.entered != nil {
		select {
		case f.entered <- struct{}{}:
		default:
		}
		select {
		case <-f.release:
		case <-ctx.Done():
			return "", ctx.Err()
		}
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	f.pcm = append(f.pcm, b...)
	return "provisional", f.feedErr
}
func (f *finalFake) StopStream(ctx context.Context) (string, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.stops++
	return f.final, f.stopErr
}
func (f *finalFake) AbortStream(context.Context) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.aborts++
	return nil
}
func (f *finalFake) Transcribe(ctx context.Context, path string) (string, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.replays++
	f.replayPath = path
	return "complete replay", ctx.Err()
}
func finalWAV(t *testing.T, samples []int16) (string, []byte) {
	t.Helper()
	path := filepath.Join(t.TempDir(), "original.wav")
	if err := audio.WriteWAV(path, samples, 16000); err != nil {
		t.Fatal(err)
	}
	b := make([]byte, len(samples)*2)
	for i, v := range samples {
		binary.LittleEndian.PutUint16(b[i*2:], uint16(v))
	}
	return path, b
}
func newFinal(t *testing.T, f Transcriber, mode models.FinalMode) FinalSession {
	t.Helper()
	s, e := NewFinalSession(t.Context(), f, FinalSessionOptions{Mode: mode})
	if e != nil {
		t.Fatal(e)
	}
	t.Cleanup(func() { _ = s.Cancel(context.Background()) })
	return s
}

func TestFinalStreamingTailExactNoReplay(t *testing.T) {
	for _, n := range []int{0, 1, 1440, 1601} {
		t.Run(string(rune(n+65)), func(t *testing.T) {
			samples := make([]int16, n)
			for i := range samples {
				samples[i] = int16(i)
			}
			path, b := finalWAV(t, samples)
			f := &finalFake{final: "last word"}
			s := newFinal(t, f, models.FinalStreaming)
			prefix := len(b) / 4 * 2
			if e := s.Feed(b[:prefix]); e != nil {
				t.Fatal(e)
			}
			r, e := s.Finish(t.Context(), path)
			if e != nil {
				t.Fatal(e)
			}
			if r.Text != "last word" || r.ActualMode != models.FinalStreaming || f.replays != 0 || f.stops != 1 || !bytes.Equal(f.pcm, b) {
				t.Fatalf("result=%+v fake=%+v", r, f)
			}
			if r.Stats.CapturedSamples != int64(n) || r.Stats.LiveSamples != int64(n) || r.Stats.TailSamples != int64(n-prefix/2) {
				t.Fatalf("stats %+v", r.Stats)
			}
			if _, e = s.Finish(t.Context(), path); e == nil {
				t.Fatal("second Finish accepted")
			}
			if e = s.Feed(nil); e == nil {
				t.Fatal("Feed after Finish accepted")
			}
			if _, e = os.Stat(path); e != nil {
				t.Fatal("original WAV deleted")
			}
		})
	}
}
func TestFinalStreamingFailuresReplayWholeOriginal(t *testing.T) {
	for _, kind := range []string{"start", "feed", "stop", "coverage", "odd", "overload", "missing-reader", "malformed"} {
		t.Run(kind, func(t *testing.T) {
			path, b := finalWAV(t, []int16{1, 2, 3, 4, 5})
			f := &finalFake{final: "must discard"}
			boom := errors.New("broken")
			switch kind {
			case "start":
				f.startErr = boom
			case "feed":
				f.feedErr = boom
			case "stop":
				f.stopErr = boom
			}
			s := newFinal(t, f, models.FinalStreaming)
			switch kind {
			case "coverage":
				_ = s.Feed([]byte{9, 9})
			case "odd":
				_ = s.Feed([]byte{1})
			case "overload":
				_ = s.Feed(make([]byte, 2*(finalBudgetSamples+1)))
			case "missing-reader":
				s.FailLive("ChunkReader unavailable")
			case "malformed":
				if e := os.WriteFile(path, []byte("invalid"), 0600); e != nil {
					t.Fatal(e)
				}
			default:
				_ = s.Feed(b)
			}
			r, e := s.Finish(t.Context(), path)
			if e != nil {
				t.Fatal(e)
			}
			if r.ActualMode != models.FinalAfterStop || r.ReplayReason == "" || r.Text != "complete replay" || f.replays != 1 || f.replayPath != path {
				t.Fatalf("%+v %+v", r, f)
			}
			if kind != "start" && f.aborts == 0 {
				t.Fatal("failed stream not aborted before replay")
			}
		})
	}
}
func TestFinalEmptySuccessDoesNotReplay(t *testing.T) {
	path, _ := finalWAV(t, nil)
	f := &finalFake{}
	s := newFinal(t, f, models.FinalStreaming)
	r, e := s.Finish(t.Context(), path)
	if e != nil || r.Text != "" || f.replays != 0 {
		t.Fatalf("%+v %v", r, e)
	}
}
func TestFinalCancelJoinsHeldFeedAndNeverReplays(t *testing.T) {
	f := &finalFake{entered: make(chan struct{}, 1), release: make(chan struct{})}
	s := newFinal(t, f, models.FinalStreaming)
	_ = s.Feed([]byte{1, 0})
	<-f.entered
	if e := s.Cancel(t.Context()); e != nil {
		t.Fatal(e)
	}
	if e := s.Cancel(t.Context()); e != nil {
		t.Fatal(e)
	}
	if f.replays != 0 || f.stops != 0 || f.aborts == 0 {
		t.Fatalf("%+v", f)
	}
	// A new recording owns clean state, not the cancelled recording's audio.
	path, b := finalWAV(t, []int16{7})
	f.entered = nil
	f.final = "fresh"
	s2 := newFinal(t, f, models.FinalStreaming)
	r, e := s2.Finish(t.Context(), path)
	if e != nil || r.Text != "fresh" || !bytes.Equal(f.pcm, b) {
		t.Fatalf("%+v %v", r, e)
	}
}

type segmentFake struct {
	mu       sync.Mutex
	samples  []int16
	paths    []string
	original string
	replay   int
	entered  chan struct{}
}

func (f *segmentFake) Transcribe(ctx context.Context, path string) (string, error) {
	samples, e := audio.ReadWAVSamples(path)
	if e != nil {
		return "", e
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	if path == f.original {
		f.replay++
		return "complete original", nil
	}
	f.samples = append(f.samples, samples...)
	f.paths = append(f.paths, path)
	if f.entered != nil {
		select {
		case f.entered <- struct{}{}:
		default:
		}
	}
	return "yes yes", ctx.Err()
}
func TestFinalSegmentsCoverageQuietTailAndRepetition(t *testing.T) {
	samples := make([]int16, 2*16000+1601)
	for i := 0; i < 16000; i++ {
		samples[i] = 10000
	}
	samples[len(samples)-1] = 22
	path, b := finalWAV(t, samples)
	f := &segmentFake{original: path, entered: make(chan struct{}, 1)}
	s := newFinal(t, f, models.FinalSegments)
	// Feed uneven even byte lengths. A completed segment must decode BEFORE
	// release, independently of the caller/producer cadence.
	for i := 0; i < 65000; i += 998 {
		end := min(i+998, 65000)
		if e := s.Feed(b[i:end]); e != nil {
			t.Fatal(e)
		}
	}
	<-f.entered
	r, e := s.Finish(t.Context(), path)
	if e != nil {
		t.Fatal(e)
	}
	if r.Text != "yes yes yes yes" || r.Stats.Segments != 2 || f.replay != 0 || len(f.samples) != len(samples) {
		t.Fatalf("%+v samples=%d", r, len(f.samples))
	}
	for i := range samples {
		if samples[i] != f.samples[i] {
			t.Fatalf("sample %d lost", i)
		}
	}
	for _, p := range f.paths {
		if _, e := os.Stat(p); !os.IsNotExist(e) {
			t.Fatalf("segment temp leaked: %s", p)
		}
	}
}
func TestFinalSegmentsUnsplittableReplays(t *testing.T) {
	samples := make([]int16, 12*16000)
	for i := range samples {
		samples[i] = 10000
	}
	path, b := finalWAV(t, samples)
	f := &segmentFake{original: path}
	s := newFinal(t, f, models.FinalSegments)
	_ = s.Feed(b)
	r, e := s.Finish(t.Context(), path)
	if e != nil || r.ActualMode != models.FinalAfterStop || !strings.Contains(r.ReplayReason, "safe segment") || f.replay != 1 {
		t.Fatalf("%+v %v", r, e)
	}
}
func TestFinalModesConstructorAndDefault(t *testing.T) {
	if _, e := NewFinalSession(t.Context(), &Mock{}, FinalSessionOptions{Mode: models.FinalStreaming}); e == nil {
		t.Fatal("nonstreaming accepted")
	}
	if _, e := NewFinalSession(t.Context(), &Mock{}, FinalSessionOptions{Mode: "typo"}); e == nil {
		t.Fatal("invalid mode accepted")
	}
	path, _ := finalWAV(t, nil)
	f := &finalFake{}
	s := newFinal(t, f, "")
	r, e := s.Finish(t.Context(), path)
	if e != nil || r.ActualMode != models.FinalAfterStop || f.replays != 1 {
		t.Fatalf("%+v %v", r, e)
	}
}

func TestFinalFeedIsBoundedNonblockingWhileInferenceHeld(t *testing.T) {
	f := &finalFake{entered: make(chan struct{}, 1), release: make(chan struct{})}
	s := newFinal(t, f, models.FinalStreaming)
	if e := s.Feed(make([]byte, 960)); e != nil {
		t.Fatal(e)
	}
	<-f.entered
	// Inference is deliberately held. Feed and Snapshot must still return,
	// counting in-flight bytes toward the same limit as queued bytes.
	if e := s.Feed(make([]byte, (finalBudgetSamples-480)*2)); e != nil {
		t.Fatal(e)
	}
	stats := s.Snapshot()
	if stats.PendingSamplesAtRelease != finalBudgetSamples || stats.PeakPendingSamples != finalBudgetSamples {
		t.Fatalf("%+v", stats)
	}
	if e := s.Feed([]byte{0, 0}); e == nil {
		t.Fatal("unbounded backlog accepted")
	}
	if !s.Snapshot().Overloaded {
		t.Fatal("overload not sticky")
	}
	if e := s.Cancel(t.Context()); e != nil {
		t.Fatal(e)
	}
}

func TestFinalSegmentsZeroAndQuietShortTail(t *testing.T) {
	for _, n := range []int{0, 1, 1440} {
		samples := make([]int16, n)
		path, _ := finalWAV(t, samples)
		f := &segmentFake{original: path}
		s := newFinal(t, f, models.FinalSegments)
		r, e := s.Finish(t.Context(), path)
		if e != nil || r.ActualMode != models.FinalSegments || len(f.samples) != n || f.replay != 0 {
			t.Fatalf("n=%d %+v %v", n, r, e)
		}
	}
}

// A failed short segment must never substitute its accumulated partials for
// the retained complete recording, and all temporary files are removed.
type failedSegment struct {
	original string
	paths    []string
	replay   int
}

func (f *failedSegment) Transcribe(ctx context.Context, path string) (string, error) {
	if path == f.original {
		f.replay++
		return "complete retained original", nil
	}
	f.paths = append(f.paths, path)
	return "discard failed partial", errors.New("segment decode failed")
}
func TestFinalSegmentFailureReplaysAndCleansTemporaryWAV(t *testing.T) {
	path, b := finalWAV(t, []int16{1, 2, 3})
	f := &failedSegment{original: path}
	s := newFinal(t, f, models.FinalSegments)
	_ = s.Feed(b)
	r, e := s.Finish(t.Context(), path)
	if e != nil || r.ActualMode != models.FinalAfterStop || r.Text != "complete retained original" || f.replay != 1 {
		t.Fatalf("%+v %v", r, e)
	}
	for _, p := range f.paths {
		if _, e := os.Stat(p); !os.IsNotExist(e) {
			t.Fatalf("leaked %s", p)
		}
	}
}

type heldSessionFinal struct {
	*finalFake
	entered, release chan struct{}
}

func (h *heldSessionFinal) StopStream(context.Context) (string, error) {
	close(h.entered)
	<-h.release
	return "cancelled late final", nil
}
func TestFinalCancellationDuringNativeFinalizeRejectsResult(t *testing.T) {
	path, _ := finalWAV(t, nil)
	f := &heldSessionFinal{&finalFake{}, make(chan struct{}), make(chan struct{})}
	s := newFinal(t, f, models.FinalStreaming)
	finished := make(chan error, 1)
	go func() { _, e := s.Finish(context.Background(), path); finished <- e }()
	<-f.entered
	cancelled := make(chan error, 1)
	go func() { cancelled <- s.Cancel(context.Background()) }()
	// Synchronize with cancellation becoming sticky, before releasing a fake
	// native call that deliberately ignores its context.
	actual := s.(*finalSession)
	for !actual.isAbandoned() {
		runtime.Gosched()
	}
	close(f.release)
	if e := <-finished; !errors.Is(e, context.Canceled) {
		t.Fatalf("late final accepted: %v", e)
	}
	if e := <-cancelled; e != nil {
		t.Fatal(e)
	}
}
