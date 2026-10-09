package daemon

import "testing"

// A cycle that finishes before an earlier one must wait its turn: the emitted
// order is spoken order, not completion order.
func TestDispatcherEmitsInSpokenOrder(t *testing.T) {
	q := newDispatcher()
	q.register(1)
	q.complete(2, &dispatchOutcome{text: "second"})
	if _, out, ok := q.pop(); ok {
		t.Fatalf("popped %q before the first cycle finished", out.text)
	}
	q.complete(1, &dispatchOutcome{text: "first"})
	_, out, ok := q.pop()
	if !ok || out.text != "first" {
		t.Fatalf("first pop = %v %v, want \"first\"", out, ok)
	}
	_, out, ok = q.pop()
	if !ok || out.text != "second" {
		t.Fatalf("second pop = %v %v, want \"second\"", out, ok)
	}
	if _, _, ok := q.pop(); ok {
		t.Fatal("popped an outcome that was not completed")
	}
}

// A cycle that produced nothing (empty, non-speech, failed) still advances the
// frontier so a later cycle is not wedged behind it forever.
func TestDispatcherEmptyCycleAdvancesTheFrontier(t *testing.T) {
	q := newDispatcher()
	q.register(1)
	q.complete(1, &dispatchOutcome{})
	q.complete(2, &dispatchOutcome{text: "typed"})

	_, out, ok := q.pop()
	if !ok || out.text != "" {
		t.Fatalf("first pop = %v %v, want an empty outcome", out, ok)
	}
	_, out, ok = q.pop()
	if !ok || out.text != "typed" {
		t.Fatalf("second pop = %v %v, want \"typed\"", out, ok)
	}
}

// A cycle parked behind a not-yet-finished predecessor waits even though a
// later cycle has already finished and completed.
func TestDispatcherParksAcrossAGap(t *testing.T) {
	q := newDispatcher()
	q.register(1)
	q.complete(3, &dispatchOutcome{text: "third"})
	if _, _, ok := q.pop(); ok {
		t.Fatal("popped a later cycle while an earlier one is outstanding")
	}
	q.complete(1, &dispatchOutcome{})
	if _, out, ok := q.pop(); !ok || out.text != "" {
		t.Fatalf("pop = %v %v, want the first (empty) cycle", out, ok)
	}
	q.complete(2, &dispatchOutcome{text: "second"})
	if _, out, ok := q.pop(); !ok || out.text != "second" {
		t.Fatalf("pop = %v %v, want \"second\"", out, ok)
	}
	if _, out, ok := q.pop(); !ok || out.text != "third" {
		t.Fatalf("pop = %v %v, want \"third\"", out, ok)
	}
}
