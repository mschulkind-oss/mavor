// Package state holds the daemon's three-state finite state machine
// (Idle → Recording → Transcribing → Idle) and the listener plumbing the
// overlay uses to redraw on every transition. It is pure Go and has no
// dependencies outside the standard library so the FSM can be exercised
// without any Wayland/audio harness.
package state

import "sync"

type State int

const (
	Idle State = iota
	Recording
	Transcribing
)

func (s State) String() string {
	switch s {
	case Idle:
		return "idle"
	case Recording:
		return "recording"
	case Transcribing:
		return "transcribing"
	}
	return "unknown"
}

type Event int

const (
	// EventToggle is what the user-facing hotkey produces. Its meaning depends
	// on current state: start recording, stop+transcribe, or no-op while a
	// transcription is in flight.
	EventToggle Event = iota
	// EventRecordStart explicitly starts recording from idle; no-op otherwise.
	EventRecordStart
	// EventRecordStop explicitly stops recording and starts transcribing; no-op otherwise.
	EventRecordStop
	// EventTranscribeDone signals that whisper finished and text was emitted.
	EventTranscribeDone
	// EventTranscribeFailed signals that whisper errored. Same effect on the
	// FSM as Done — return to Idle — but reported separately so callers can
	// surface the error.
	EventTranscribeFailed
)

type Machine struct {
	mu        sync.Mutex
	state     State
	nextID    uint64
	listeners map[uint64]func(State)
}

func New() *Machine {
	return &Machine{
		state:     Idle,
		listeners: make(map[uint64]func(State)),
	}
}

func (m *Machine) State() State {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.state
}

// Apply runs the transition for the given event and returns the resulting
// state plus a boolean indicating whether the state changed. Listeners run
// only on changes.
func (m *Machine) Apply(e Event) (State, bool) {
	m.mu.Lock()
	prev := m.state
	next := transition(prev, e)
	m.state = next
	listeners := make([]func(State), 0, len(m.listeners))
	if next != prev {
		for _, fn := range m.listeners {
			listeners = append(listeners, fn)
		}
	}
	m.mu.Unlock()

	for _, fn := range listeners {
		fn(next)
	}
	return next, next != prev
}

// Subscribe registers fn to be called after every state change. It runs while
// the FSM lock is NOT held, so listeners may call back into State() without
// deadlock. Returns an unsubscribe function.
func (m *Machine) Subscribe(fn func(State)) func() {
	m.mu.Lock()
	id := m.nextID
	m.nextID++
	m.listeners[id] = fn
	m.mu.Unlock()
	return func() {
		m.mu.Lock()
		delete(m.listeners, id)
		m.mu.Unlock()
	}
}

func transition(s State, e Event) State {
	switch s {
	case Idle:
		if e == EventToggle || e == EventRecordStart {
			return Recording
		}
	case Recording:
		if e == EventToggle || e == EventRecordStop {
			return Transcribing
		}
		if e == EventTranscribeFailed {
			return Idle
		}
	case Transcribing:
		if e == EventTranscribeDone || e == EventTranscribeFailed {
			return Idle
		}
		// EventToggle, EventRecordStart, and EventRecordStop while transcribing are intentionally no-ops.
	}
	return s
}
