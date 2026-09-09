package overlay

import (
	"io"
	"log/slog"
	"path/filepath"
	"testing"
)

// A rebuild that cannot reach a compositor must leave the state it was given
// exactly as it found it.
//
// rebuild used to destroy the surface and close the connection FIRST and only
// then dial a fresh one. When the dial failed — which is precisely when an
// output has just gone away, the case rebuild exists for — it returned an
// error having already closed the display, and st was left pointing at a dead
// connection. The loop logged "could not be rebuilt yet", scheduled a retry
// two seconds out, and on its very next tick called DispatchPending on that
// closed connection, got "use of closed network connection", and stopped for
// good. The retry it had just scheduled never ran: the overlay was gone until
// the daemon restarted, while recording, ducking and typing carried on.
//
// The nil display and surface here are the assertion. Under the old order the
// teardown ran before the dial was known to have failed, so this panicked;
// under the new one a failed rebuild returns before touching either, which is
// what keeps the connection alive for the retry to use.
func TestFailedRebuildLeavesTheOldConnectionAlone(t *testing.T) {
	// An absolute path to a socket that does not exist, so Connect fails on
	// the dial rather than finding whatever compositor is running the tests.
	t.Setenv("WAYLAND_DISPLAY", filepath.Join(t.TempDir(), "no-such-compositor"))

	o := &WL{log: slog.New(slog.NewTextHandler(io.Discard, nil))}
	st := &wlState{}

	if err := o.rebuild(st); err == nil {
		t.Fatal("rebuild reported success with no compositor to connect to")
	}
	if st.display != nil || st.surface != nil {
		t.Error("a failed rebuild replaced the state it was given; the old connection must survive for the retry")
	}
}
