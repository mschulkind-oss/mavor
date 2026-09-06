package main

import (
	"context"
	"errors"
	"sync/atomic"
	"testing"
	"time"
)

type fakeEngine struct {
	delay   time.Duration
	err     error
	running atomic.Bool
	peak    *atomic.Int32
	live    *atomic.Int32
}

func (f *fakeEngine) Start(ctx context.Context) error {
	f.running.Store(true)
	if f.live != nil {
		n := f.live.Add(1)
		for {
			p := f.peak.Load()
			if n <= p || f.peak.CompareAndSwap(p, n) {
				break
			}
		}
	}
	time.Sleep(f.delay)
	if f.live != nil {
		f.live.Add(-1)
	}
	return f.err
}

// The point of beginStart: two independent model loads overlap instead of
// queueing. Before this, a daemon's cold start cost main + companion, and
// both are multi-hundred-megabyte loads.
func TestTheTwoModelLoadsOverlap(t *testing.T) {
	const delay = 120 * time.Millisecond
	var peak, live atomic.Int32

	main := &fakeEngine{delay: delay, peak: &peak, live: &live}
	companion := &fakeEngine{delay: delay, peak: &peak, live: &live}

	start := time.Now()
	wait := beginStart(context.Background(), main)
	// Stands in for speech.LoadPreview, which loads and warms the companion
	// on the caller's own goroutine.
	if err := companion.Start(context.Background()); err != nil {
		t.Fatal(err)
	}
	if err := wait(); err != nil {
		t.Fatal(err)
	}
	elapsed := time.Since(start)

	t.Logf("two %v loads took %v; peak concurrent loads %d", delay, elapsed, peak.Load())
	if peak.Load() < 2 {
		t.Errorf("peak concurrent loads was %d, want 2: the loads are still running in series", peak.Load())
	}
	if elapsed >= 2*delay {
		t.Errorf("two %v loads took %v, which is the serial cost — they did not overlap", delay, elapsed)
	}
}

// A failed engine start is fatal, and moving it onto a goroutine must not
// lose the error or the fact that it was the engine that failed.
func TestABackgroundStartFailureStillReachesTheCaller(t *testing.T) {
	boom := errors.New("no such model file")
	wait := beginStart(context.Background(), &fakeEngine{err: boom})
	err := wait()
	if !errors.Is(err, boom) {
		t.Fatalf("wait() = %v, want it to wrap %v", err, boom)
	}
	if got := err.Error(); got != "start transcriber engine: no such model file" {
		t.Errorf("error reads %q, which does not say what failed", got)
	}
}

// Not every transcriber has an engine to bring up — whisper-cli is a
// subprocess per utterance and implements no Start.
func TestATranscriberWithNoEngineWaitsForNothing(t *testing.T) {
	if err := beginStart(context.Background(), struct{}{})(); err != nil {
		t.Fatalf("wait() = %v, want nil", err)
	}
}
