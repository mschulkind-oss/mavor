package daemon

import "sync"

// dispatchOutcome is one finished cycle's contribution to the output stream. A
// zero Text means the cycle produced nothing to type — empty, non-speech, or
// failed — and still advances spoken order so one silent dictation cannot wedge
// the queue. keepError leaves the error pill up for errorDuration once the
// cycle is retired.
type dispatchOutcome struct {
	text      string
	source    string
	model     string
	warning   string
	keepError bool
}

// dispatcher emits finished transcripts in spoken order — strictly increasing
// cycle id — never in completion order. Typing is the one place order is
// irrecoverable if it slips: a follow-up thought typed ahead of the sentence it
// answers corrupts the prose and cannot be reconstructed. So a cycle that
// finishes before an earlier one parks here until its turn, and a cycle with
// nothing to type still advances the frontier.
type dispatcher struct {
	mu     sync.Mutex
	next   uint64
	parked map[uint64]*dispatchOutcome
	wake   chan struct{}
}

func newDispatcher() *dispatcher {
	return &dispatcher{parked: make(map[uint64]*dispatchOutcome), wake: make(chan struct{}, 1)}
}

// register pins the frontier to the smallest cycle id registered — the first
// dictation the daemon starts. Ids arrive in increasing order, so a later id
// leaves the frontier alone; a lower id moves it backwards rather than letting
// a cycle be skipped.
func (q *dispatcher) register(id uint64) {
	q.mu.Lock()
	if q.next == 0 || id < q.next {
		q.next = id
	}
	q.mu.Unlock()
}

// complete records a finished cycle and wakes the output goroutine. It never
// blocks on Emit; the dispatcher goroutine owns output.
func (q *dispatcher) complete(id uint64, out *dispatchOutcome) {
	q.mu.Lock()
	q.parked[id] = out
	q.mu.Unlock()
	select {
	case q.wake <- struct{}{}:
	default:
	}
}

// pop returns the next cycle in spoken order. It reports false when the next
// cycle has not finished yet.
func (q *dispatcher) pop() (uint64, *dispatchOutcome, bool) {
	q.mu.Lock()
	defer q.mu.Unlock()
	out, ok := q.parked[q.next]
	if !ok {
		return 0, nil, false
	}
	id := q.next
	delete(q.parked, id)
	q.next++
	return id, out, true
}
