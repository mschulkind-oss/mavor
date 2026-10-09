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
	if got := New().Snapshot(); got.RecordingID != 0 || len(got.Pending) != 0 || got.Phase != PhaseReady {
		t.Fatalf("New().Snapshot() = %+v, want ready with no cycles", got)
	}
}

// recordingMachine is a machine already recording cycle 1.
func recordingMachine() *Machine {
	m := New()
	m.recordingID = 1
	m.nextCycleID = 1
	return m
}

// transcribingMachine is a machine with cycle 1 stopped and awaiting output.
func transcribingMachine() *Machine {
	m := New()
	m.pending = []uint64{1}
	m.nextCycleID = 1
	return m
}

// recordingPendingMachine is recording cycle 2 while cycle 1 awaits output.
func recordingPendingMachine() *Machine {
	m := New()
	m.recordingID = 2
	m.pending = []uint64{1}
	m.nextCycleID = 2
	return m
}

func initializingMachine() *Machine { m := New(); m.phase = PhaseInitializing; return m }
func failedMachine() *Machine       { m := New(); m.phase = PhaseFailed; return m }

// Apply is the legacy entry point. The behavior change stacking ships: a toggle
// or start while transcribing now starts a fresh recording instead of being a
// silent no-op.
func TestApplyTransitions(t *testing.T) {
	cases := []struct {
		name    string
		make    func() *Machine
		event   Event
		want    State
		changed bool
	}{
		{"toggle in idle starts recording", New, EventToggle, Recording, true},
		{"toggle in recording stops it", recordingMachine, EventToggle, Transcribing, true},
		{"toggle in transcribing starts a new recording", transcribingMachine, EventToggle, Recording, true},
		{"toggle in recording with a pending cycle stops it", recordingPendingMachine, EventToggle, Transcribing, true},
		{"transcribe-done in transcribing returns to idle", transcribingMachine, EventTranscribeDone, Idle, true},
		{"transcribe-done with a pending cycle and a recording stays recording", recordingPendingMachine, EventTranscribeDone, Recording, true},
		{"transcribe-done in idle is a no-op", New, EventTranscribeDone, Idle, false},
		{"transcribe-done in recording without pending is a no-op", recordingMachine, EventTranscribeDone, Recording, false},
		{"transcribe-failed in transcribing returns to idle", transcribingMachine, EventTranscribeFailed, Idle, true},
		{"start in idle starts recording", New, EventRecordStart, Recording, true},
		{"start in recording is a no-op", recordingMachine, EventRecordStart, Recording, false},
		{"start in transcribing starts a new recording", transcribingMachine, EventRecordStart, Recording, true},
		{"stop in idle is a no-op", New, EventRecordStop, Idle, false},
		{"stop in recording starts transcribing", recordingMachine, EventRecordStop, Transcribing, true},
		{"stop in transcribing is a no-op", transcribingMachine, EventRecordStop, Transcribing, false},
		{"initialize in idle enters initializing", New, EventInitialize, Initializing, true},
		{"ready leaves initializing", initializingMachine, EventReady, Idle, true},
		{"initialize-failed enters failed", initializingMachine, EventInitializeFailed, Failed, true},
		{"ready is refused outside initializing", New, EventReady, Idle, false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			m := tc.make()
			got, changed := m.Apply(tc.event)
			if got != tc.want || changed != tc.changed {
				t.Fatalf("Apply(%v) = (%v, %v), want (%v, %v)", tc.event, got, changed, tc.want, tc.changed)
			}
			if cur := m.State(); cur != tc.want {
				t.Fatalf("State() after Apply = %v, want %v", cur, tc.want)
			}
		})
	}
}

func TestRecordStartStopFinishCarriesCycleIdentity(t *testing.T) {
	m := New()
	id1, snap, changed := m.RecordStart()
	if !changed || id1 != 1 || snap.RecordingID != 1 {
		t.Fatalf("RecordStart = (%d, %+v, %v), want id 1 recording", id1, snap, changed)
	}
	// A second start while recording is refused and does not allocate an id.
	again, _, changed := m.RecordStart()
	if changed || again != 0 {
		t.Fatalf("RecordStart while recording = (%d, %v), want refused", again, changed)
	}

	stopped, snap, changed := m.RecordStop()
	if !changed || stopped != 1 {
		t.Fatalf("RecordStop = (%d, %v), want cycle 1", stopped, changed)
	}
	if snap.RecordingID != 0 || len(snap.Pending) != 1 || snap.Pending[0] != 1 || snap.State() != Transcribing {
		t.Fatalf("snapshot after stop = %+v", snap)
	}

	// Finish retires exactly the id it is given, and is idempotent.
	if snap, changed := m.Finish(1); !changed || snap.State() != Idle {
		t.Fatalf("Finish(1) = (%+v, %v), want idle", snap, changed)
	}
	if snap, changed := m.Finish(1); changed || snap.State() != Idle {
		t.Fatalf("second Finish(1) = (%+v, %v), want idempotent no-op", snap, changed)
	}
}

// Two recordings may exist back to back: the second starts while the first
// awaits output, and Idle returns only when the last finishes.
func TestStackedCyclesReturnToIdleOnTheLast(t *testing.T) {
	m := New()
	if _, _, changed := m.RecordStart(); !changed {
		t.Fatal("first start refused")
	}
	if _, _, changed := m.RecordStop(); !changed {
		t.Fatal("first stop refused")
	}
	// Starting again while cycle 1 is pending is the whole feature.
	id2, snap, changed := m.RecordStart()
	if !changed || id2 != 2 || snap.RecordingID != 2 || len(snap.Pending) != 1 {
		t.Fatalf("stacked start = (%d, %+v, %v)", id2, snap, changed)
	}
	if _, _, changed := m.RecordStop(); !changed {
		t.Fatal("second stop refused")
	}
	if got := m.State(); got != Transcribing {
		t.Fatalf("state with two pending = %v, want transcribing", got)
	}
	if snap, changed := m.Finish(1); !changed || snap.State() != Transcribing {
		t.Fatalf("Finish(1) = (%+v), want still transcribing", snap)
	}
	if snap, changed := m.Finish(2); !changed || snap.State() != Idle {
		t.Fatalf("Finish(2) = (%+v), want idle", snap)
	}
}

func TestPendingCapRefusesStart(t *testing.T) {
	m := New()
	for i := 0; i < MaxPending; i++ {
		if _, _, changed := m.RecordStart(); !changed {
			t.Fatalf("start %d refused below the cap", i+1)
		}
		if _, _, changed := m.RecordStop(); !changed {
			t.Fatalf("stop %d refused", i+1)
		}
	}
	if snap := m.Snapshot(); len(snap.Pending) != MaxPending {
		t.Fatalf("pending = %d, want %d", len(snap.Pending), MaxPending)
	}
	id, snap, changed := m.RecordStart()
	if changed || id != 0 || snap.RecordingID != 0 {
		t.Fatalf("RecordStart at the cap = (%d, %v), want refused and no recording", id, changed)
	}
	// Retiring one cycle makes room again.
	if _, changed := m.Finish(1); !changed {
		t.Fatal("Finish refused")
	}
	if _, snap, changed := m.RecordStart(); !changed || snap.RecordingID == 0 {
		t.Fatalf("start after making room refused: %+v %v", snap, changed)
	}
}

func TestAbortRecordingClearsTheSlotWithoutQueueing(t *testing.T) {
	m := recordingMachine()
	snap, changed := m.AbortRecording()
	if !changed || snap.State() != Idle || len(snap.Pending) != 0 {
		t.Fatalf("AbortRecording = (%+v, %v), want idle with nothing pending", snap, changed)
	}
	if _, changed := m.AbortRecording(); changed {
		t.Fatal("AbortRecording with no recording must be a no-op")
	}
}

// A start/stop/finish in Initializing or Failed is refused; recording cannot
// leak into or out of the lifecycle phases.
func TestPhasesRefuseRecording(t *testing.T) {
	for _, m := range []*Machine{initializingMachine(), failedMachine()} {
		if _, _, changed := m.RecordStart(); changed {
			t.Fatalf("RecordStart in %v changed state", m.State())
		}
		if _, _, changed := m.RecordStop(); changed {
			t.Fatalf("RecordStop in %v changed state", m.State())
		}
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

	m.RecordStart() // Idle -> Recording
	m.RecordStop()  // Recording -> Transcribing
	m.RecordStart() // Transcribing -> Recording (stacking)
	m.RecordStop()  // Recording -> Transcribing
	m.Finish(1)     // Transcribing -> Transcribing (must not notify)
	m.Finish(2)     // Transcribing -> Idle

	mu.Lock()
	defer mu.Unlock()
	want := []State{Recording, Transcribing, Recording, Transcribing, Idle}
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

	m.RecordStart() // Idle -> Recording (1 notification)
	unsub()
	m.RecordStop() // Recording -> Transcribing (must not notify)

	if got := atomic.LoadInt32(&count); got != 1 {
		t.Fatalf("listener called %d times after unsubscribe, want 1", got)
	}
}

func TestConcurrentCycleOperationsAreRaceFree(t *testing.T) {
	m := New()
	var wg sync.WaitGroup
	for range 50 {
		wg.Go(func() {
			id, _, ok := m.RecordStart()
			if ok {
				m.Finish(id)
			}
			m.RecordStop()
			m.Snapshot()
		})
	}
	wg.Wait()
	// We don't assert a final state — only that the cycle operations are safe
	// under concurrent use. The -race detector flags violations.
}
