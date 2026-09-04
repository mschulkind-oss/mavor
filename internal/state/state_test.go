package state

import (
	"sync"
	"sync/atomic"
	"testing"
)

func TestNewIsIdle(t *testing.T) {
	if got := New().State(); got != Idle {
		t.Fatalf("New().State() = %v, want %v", got, Idle)
	}
}

func TestApplyTransitions(t *testing.T) {
	cases := []struct {
		name    string
		from    State
		event   Event
		want    State
		changed bool
	}{
		{"toggle in idle starts recording", Idle, EventToggle, Recording, true},
		{"toggle in recording starts transcribing", Recording, EventToggle, Transcribing, true},
		{"transcribe-failed in recording returns to idle", Recording, EventTranscribeFailed, Idle, true},
		{"toggle in transcribing is a no-op", Transcribing, EventToggle, Transcribing, false},
		{"transcribe-done in transcribing returns to idle", Transcribing, EventTranscribeDone, Idle, true},
		{"transcribe-failed in transcribing returns to idle", Transcribing, EventTranscribeFailed, Idle, true},
		{"transcribe-done in idle is a no-op", Idle, EventTranscribeDone, Idle, false},
		{"transcribe-done in recording is a no-op", Recording, EventTranscribeDone, Recording, false},
		{"start in idle starts recording", Idle, EventRecordStart, Recording, true},
		{"start in recording is a no-op", Recording, EventRecordStart, Recording, false},
		{"start in transcribing is a no-op", Transcribing, EventRecordStart, Transcribing, false},
		{"stop in idle is a no-op", Idle, EventRecordStop, Idle, false},
		{"stop in recording starts transcribing", Recording, EventRecordStop, Transcribing, true},
		{"stop in transcribing is a no-op", Transcribing, EventRecordStop, Transcribing, false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			m := newAt(tc.from)
			got, changed := m.Apply(tc.event)
			if got != tc.want || changed != tc.changed {
				t.Fatalf("Apply(%v) on %v = (%v, %v), want (%v, %v)",
					tc.event, tc.from, got, changed, tc.want, tc.changed)
			}
			if cur := m.State(); cur != tc.want {
				t.Fatalf("State() after Apply = %v, want %v", cur, tc.want)
			}
		})
	}
}

func TestSubscribeReceivesTransitions(t *testing.T) {
	m := New()
	var received []State
	var mu sync.Mutex
	m.Subscribe(func(s State) {
		mu.Lock()
		received = append(received, s)
		mu.Unlock()
	})

	m.Apply(EventToggle)         // Idle -> Recording
	m.Apply(EventToggle)         // Recording -> Transcribing
	m.Apply(EventToggle)         // no-op, must not notify
	m.Apply(EventTranscribeDone) // Transcribing -> Idle

	mu.Lock()
	defer mu.Unlock()
	want := []State{Recording, Transcribing, Idle}
	if len(received) != len(want) {
		t.Fatalf("got %d notifications, want %d (%v)", len(received), len(want), received)
	}
	for i := range want {
		if received[i] != want[i] {
			t.Fatalf("notification[%d] = %v, want %v", i, received[i], want[i])
		}
	}
}

func TestUnsubscribeStopsNotifications(t *testing.T) {
	m := New()
	var count int32
	unsub := m.Subscribe(func(State) { atomic.AddInt32(&count, 1) })

	m.Apply(EventToggle) // Idle -> Recording (1 notification)
	unsub()
	m.Apply(EventToggle) // Recording -> Transcribing (must not notify)

	if got := atomic.LoadInt32(&count); got != 1 {
		t.Fatalf("listener called %d times after unsubscribe, want 1", got)
	}
}

func TestConcurrentApplyIsRaceFree(t *testing.T) {
	m := New()
	var wg sync.WaitGroup
	for range 50 {
		wg.Go(func() {
			m.Apply(EventToggle)
			m.Apply(EventTranscribeDone)
		})
	}
	wg.Wait()
	// We don't assert a final state — only that Apply is safe under concurrent
	// use. The -race detector flags violations.
}

// newAt is a test helper that constructs a Machine already in the given state,
// without exercising Apply. Lets each table case start from any state.
func newAt(s State) *Machine {
	m := New()
	m.state = s
	return m
}
