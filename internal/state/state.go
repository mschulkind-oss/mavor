// Package state holds the daemon's finite state machine: initialization reaches
// Ready or Failed; dictation cycles move through a recording slot and an ordered
// queue of stopped cycles awaiting transcription. The listener plumbing lets a
// consumer redraw on every derived-state change. It is pure Go and has no
// dependencies outside the standard library so the FSM can be exercised
// without any Wayland/audio harness.
//
// A single State cannot express "recording and transcribing": stacking lets the
// next recording start while earlier ones still decode. The machine therefore
// tracks cycle identity — one RecordingID and an ordered Pending queue — and
// derives the coarse State (the value on the IPC wire) from that snapshot.
//
// Architecture and invariants: docs/reference/how-mavor-works.md
package state

import "sync"

type State int

const (
	Idle State = iota
	Recording
	Transcribing
	Initializing
	Failed
)

func (s State) String() string {
	switch s {
	case Initializing:
		return "initializing"
	case Failed:
		return "failed"
	case Idle:
		return "idle"
	case Recording:
		return "recording"
	case Transcribing:
		return "transcribing"
	}
	return "unknown"
}

// Phase is the daemon lifecycle phase, independent of dictation cycles. The
// zero value is Ready so a zero Machine is usable.
type Phase int

const (
	PhaseReady Phase = iota
	PhaseInitializing
	PhaseFailed
)

func (p Phase) String() string {
	switch p {
	case PhaseInitializing:
		return "initializing"
	case PhaseFailed:
		return "failed"
	}
	return "ready"
}

type Event int

const (
	// EventToggle is what the user-facing hotkey produces. It advances the
	// recording slot: start recording if none is active, otherwise stop it and
	// queue it for transcription. A pending transcription never blocks it.
	EventToggle Event = iota
	// EventRecordStart explicitly starts recording from idle; no-op otherwise.
	EventRecordStart
	// EventRecordStop explicitly stops the active recording and queues it; no-op otherwise.
	EventRecordStop
	// EventTranscribeDone signals that the oldest pending cycle finished and
	// text was emitted.
	EventTranscribeDone
	// EventTranscribeFailed signals that the oldest pending cycle errored. Same
	// effect on the FSM as Done — retire the cycle — but reported separately so
	// callers can surface the error.
	EventTranscribeFailed
	EventInitialize
	EventReady
	EventInitializeFailed
)

// MaxPending is how many stopped cycles may await transcription before a new
// recording is refused. Each holds one WAV on disk, so the queue is bounded
// rather than left to grow. It is not a config key.
const MaxPending = 8

// Snapshot is the machine's whole dictation state: the lifecycle phase, the
// single recording id (0 when none), and the ordered ids of stopped cycles
// awaiting transcription, oldest first.
type Snapshot struct {
	Phase       Phase
	RecordingID uint64
	Pending     []uint64
}

// Recording reports whether a capture is active.
func (s Snapshot) Recording() bool { return s.RecordingID != 0 }

// State derives the coarse state the IPC wire and `mavor status` name.
func (s Snapshot) State() State {
	switch s.Phase {
	case PhaseInitializing:
		return Initializing
	case PhaseFailed:
		return Failed
	}
	if s.RecordingID != 0 {
		return Recording
	}
	if len(s.Pending) > 0 {
		return Transcribing
	}
	return Idle
}

type Machine struct {
	mu             sync.Mutex
	phase          Phase
	recordingID    uint64
	pending        []uint64
	nextCycleID    uint64
	nextListenerID uint64
	listeners      map[uint64]func(State)
}

func New() *Machine {
	return &Machine{listeners: make(map[uint64]func(State))}
}

func (m *Machine) State() State { return m.Snapshot().State() }

func (m *Machine) Snapshot() Snapshot {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.snapshotLocked()
}

func (m *Machine) snapshotLocked() Snapshot {
	pending := make([]uint64, len(m.pending))
	copy(pending, m.pending)
	return Snapshot{Phase: m.phase, RecordingID: m.recordingID, Pending: pending}
}

// RecordStart opens a recording if none is active and the pending queue has
// room. id is the new cycle's identity; changed is false when refused — already
// recording, not Ready, or the queue is full. It reports the resulting snapshot
// so a caller can act on the transition without re-reading state.
func (m *Machine) RecordStart() (id uint64, snap Snapshot, changed bool) {
	m.mu.Lock()
	prev := m.snapshotLocked().State()
	if m.phase != PhaseReady || m.recordingID != 0 || len(m.pending) >= MaxPending {
		snap = m.snapshotLocked()
		m.mu.Unlock()
		return 0, snap, false
	}
	m.nextCycleID++
	m.recordingID = m.nextCycleID
	id = m.recordingID
	snap = m.snapshotLocked()
	listeners := m.listenersForLocked(prev, snap.State())
	m.mu.Unlock()
	runListeners(listeners, snap.State())
	return id, snap, true
}

// RecordStop moves the active recording into the pending queue and returns its
// id. It is a no-op when there is no recording.
func (m *Machine) RecordStop() (id uint64, snap Snapshot, changed bool) {
	m.mu.Lock()
	prev := m.snapshotLocked().State()
	if m.phase != PhaseReady || m.recordingID == 0 {
		snap = m.snapshotLocked()
		m.mu.Unlock()
		return 0, snap, false
	}
	id = m.recordingID
	m.recordingID = 0
	m.pending = append(m.pending, id)
	snap = m.snapshotLocked()
	listeners := m.listenersForLocked(prev, snap.State())
	m.mu.Unlock()
	runListeners(listeners, snap.State())
	return id, snap, true
}

// Toggle advances the recording slot. started is true when it opened a fresh
// recording, false when it stopped one; changed is false when nothing moved.
func (m *Machine) Toggle() (id uint64, snap Snapshot, started bool, changed bool) {
	m.mu.Lock()
	recording := m.recordingID != 0
	m.mu.Unlock()
	if recording {
		id, snap, changed = m.RecordStop()
		return id, snap, false, changed
	}
	id, snap, changed = m.RecordStart()
	return id, snap, changed, changed
}

// Finish retires a cycle id from the pending queue. It is idempotent: a retired
// or never-pending id returns changed=false, so a late or cancelled completion
// is harmless.
func (m *Machine) Finish(id uint64) (snap Snapshot, changed bool) {
	m.mu.Lock()
	prev := m.snapshotLocked().State()
	idx := -1
	for i, p := range m.pending {
		if p == id {
			idx = i
			break
		}
	}
	if idx < 0 {
		snap = m.snapshotLocked()
		m.mu.Unlock()
		return snap, false
	}
	m.pending = append(m.pending[:idx], m.pending[idx+1:]...)
	snap = m.snapshotLocked()
	listeners := m.listenersForLocked(prev, snap.State())
	m.mu.Unlock()
	runListeners(listeners, snap.State())
	return snap, true
}

// AbortRecording clears the active recording without queueing it. It is the
// capture-start-failure path: the cycle never produced audio, so it does not
// belong in Pending and must not wait its turn in the output order.
func (m *Machine) AbortRecording() (snap Snapshot, changed bool) {
	m.mu.Lock()
	prev := m.snapshotLocked().State()
	if m.recordingID == 0 {
		snap = m.snapshotLocked()
		m.mu.Unlock()
		return snap, false
	}
	m.recordingID = 0
	snap = m.snapshotLocked()
	listeners := m.listenersForLocked(prev, snap.State())
	m.mu.Unlock()
	runListeners(listeners, snap.State())
	return snap, true
}

// Apply runs the transition for the given event and returns the resulting
// coarse state plus a boolean indicating whether it changed. It is the legacy
// entry point over the cycle methods: the toggle family maps onto
// RecordStart/RecordStop, and a transcription completion retires the oldest
// pending cycle.
func (m *Machine) Apply(e Event) (State, bool) {
	switch e {
	case EventToggle:
		_, snap, _, changed := m.Toggle()
		return snap.State(), changed
	case EventRecordStart:
		_, snap, changed := m.RecordStart()
		return snap.State(), changed
	case EventRecordStop:
		_, snap, changed := m.RecordStop()
		return snap.State(), changed
	case EventTranscribeDone, EventTranscribeFailed:
		m.mu.Lock()
		var id uint64
		if len(m.pending) > 0 {
			id = m.pending[0]
		}
		m.mu.Unlock()
		if id == 0 {
			return m.State(), false
		}
		snap, changed := m.Finish(id)
		return snap.State(), changed
	case EventInitialize:
		return m.setPhase(PhaseInitializing, PhaseReady)
	case EventReady:
		return m.setPhase(PhaseReady, PhaseInitializing)
	case EventInitializeFailed:
		return m.setPhase(PhaseFailed, PhaseInitializing)
	}
	return m.State(), false
}

// setPhase moves the lifecycle phase only from the expected predecessor, so a
// late ready/failed cannot overwrite a terminal state.
func (m *Machine) setPhase(to, from Phase) (State, bool) {
	m.mu.Lock()
	prev := m.snapshotLocked().State()
	if m.phase != from {
		snap := m.snapshotLocked()
		m.mu.Unlock()
		return snap.State(), false
	}
	m.phase = to
	snap := m.snapshotLocked()
	listeners := m.listenersForLocked(prev, snap.State())
	m.mu.Unlock()
	runListeners(listeners, snap.State())
	return snap.State(), true
}

// Subscribe registers fn to be called after every derived-state change. It runs
// while the FSM lock is NOT held, so listeners may call back into State()
// without deadlock. Returns an unsubscribe function.
func (m *Machine) Subscribe(fn func(State)) func() {
	m.mu.Lock()
	id := m.nextListenerID
	m.nextListenerID++
	m.listeners[id] = fn
	m.mu.Unlock()
	return func() {
		m.mu.Lock()
		delete(m.listeners, id)
		m.mu.Unlock()
	}
}

// listenersForLocked returns a copy of the listeners when the derived state
// changed, or nil. The caller holds the lock.
func (m *Machine) listenersForLocked(prev, next State) []func(State) {
	if prev == next {
		return nil
	}
	out := make([]func(State), 0, len(m.listeners))
	for _, fn := range m.listeners {
		out = append(out, fn)
	}
	return out
}

func runListeners(fns []func(State), s State) {
	for _, fn := range fns {
		fn(s)
	}
}
