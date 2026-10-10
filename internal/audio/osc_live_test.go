package audio

import (
	"net"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// requireNoRunningDaemon skips a live test while a mavor daemon is up.
//
// The daemon mutes the same parameters on its own schedule, and the two are
// uncoordinated writers: this test's Restore can unmute what a dictation in
// flight just muted, the user hears the wrong audio, and the daemon's log then
// reports failures that are the test's doing rather than the device's. Stop it
// first — `systemctl --user stop mavor` — and start it again afterwards.
func requireNoRunningDaemon(t *testing.T) {
	t.Helper()
	dir := os.Getenv("XDG_RUNTIME_DIR")
	if dir == "" {
		return
	}
	sock := filepath.Join(dir, "mavor.sock")
	conn, err := net.DialTimeout("unix", sock, 200*time.Millisecond)
	if err != nil {
		return // no socket, or nothing listening on a stale one
	}
	_ = conn.Close()
	t.Skipf("a mavor daemon is listening on %s and mutes the same parameters; stop it and rerun", sock)
}

// TestLiveOSCDevice runs the real ducker against real hardware. It is skipped
// unless MAVOR_LIVE_OSC names a device, so `go test ./...` is unaffected:
//
//	MAVOR_LIVE_OSC=192.168.1.6 MAVOR_LIVE_OSC_PATHS=/ch/15/mix/on,/ch/16/mix/on \
//	  go test ./internal/audio/ -run TestLiveOSCDevice -v -count=1
//
// It exists because a fake can only confirm that the encoder agrees with the
// decoder. Every fact that made this feature work — that the port is 10024,
// that a bare address is a query, that the reply is `,i` — is a claim about
// firmware, and the only way to check a claim about firmware is to ask the
// firmware. It restores whatever it found before it started.
//
// Two things this found against a real XR18 that no fake would have:
//
//   - Reading a parameter back microseconds after writing it does not reflect
//     what the device has applied, and a burst of writes and reads inside a
//     millisecond comes back reordered. That is why this test settles between
//     steps — and why it is not a problem in the daemon, where Duck and
//     Restore are a whole dictation apart.
//   - A linked stereo pair (`/config/chlink/15-16 = 1`) moves together: a
//     write to either channel sets both. That is safe here, and measurably
//     so — such a pair CANNOT hold two different values, whichever order the
//     writes arrive in, so the two saved values are always equal and the
//     second write of a Restore can never undo the first. It is one redundant
//     datagram, not a conflict.
//
// The timing mirrors the daemon's: Duck and Restore are a dictation apart.
func TestLiveOSCDevice(t *testing.T) {
	host := os.Getenv("MAVOR_LIVE_OSC")
	if host == "" {
		t.Skip("set MAVOR_LIVE_OSC to a device address to run against real hardware")
	}
	requireNoRunningDaemon(t)
	paths := []string{"/ch/15/mix/on", "/ch/16/mix/on"}
	if p := os.Getenv("MAVOR_LIVE_OSC_PATHS"); p != "" {
		paths = strings.Split(p, ",")
	}
	const settle = 400 * time.Millisecond

	d, err := NewOSCDucker(host, 0, paths, 0, DefaultOSCTimeoutMS*time.Millisecond)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = d.Close() })

	before, missing, err := d.Probe()
	if err != nil {
		t.Fatalf("Probe: %v", err)
	}
	if len(missing) > 0 {
		t.Fatalf("device did not answer for %v — is it powered on and at %s?", missing, d.Addr())
	}
	t.Logf("before: %v", before)

	start := time.Now()
	if err := d.Duck(); err != nil {
		t.Fatalf("Duck: %v", err)
	}
	duckTook := time.Since(start)
	t.Logf("Duck took %s", duckTook.Round(time.Microsecond))
	// Duck runs on the transition into Recording, before the recorder starts,
	// so it is latency the user feels at the top of every dictation.
	if duckTook > 50*time.Millisecond {
		t.Errorf("Duck took %s against a device that answered — that is on the dictation hot path", duckTook)
	}

	time.Sleep(settle)
	muted, _, err := d.Probe()
	if err != nil {
		t.Fatalf("Probe while ducked: %v", err)
	}
	t.Logf("while ducked: %v", muted)
	for _, p := range d.Paths() {
		if muted[p] != d.MutedValue() {
			t.Errorf("%s = %d while ducked, want %d", p, muted[p], d.MutedValue())
		}
	}

	if err := d.Restore(); err != nil {
		t.Fatalf("Restore: %v", err)
	}
	time.Sleep(settle)
	after, _, err := d.Probe()
	if err != nil {
		t.Fatalf("Probe after restore: %v", err)
	}
	t.Logf("after: %v", after)
	for _, p := range d.Paths() {
		if after[p] != before[p] {
			t.Errorf("%s = %d after restore, want %d — the value it had before", p, after[p], before[p])
		}
	}
}

// TestLiveOSCRestoreImmediatelyAfterDuck runs Restore's send-and-confirm cycle
// against real firmware with no settling delay, the way the daemon runs it.
//
// The sequence it covers is the daemon's, not the test above's: Duck happens at
// the top of a dictation and Restore at the bottom, so the two are seconds
// apart — but *inside* Restore the writes and the read-back that confirms them
// are microseconds apart, and that is the window this exercises. Against an
// XR18 a read issued that soon after a write can be answered with the value
// from before it, which is not a failed restore and must not be reported as
// one.
//
// A linked stereo pair (`/config/chlink/NN-MM = 1`) is the standing case: it
// moves as one, so a read-back that races a write to either half reports the
// pre-write value for both halves at once.
func TestLiveOSCRestoreImmediatelyAfterDuck(t *testing.T) {
	host := os.Getenv("MAVOR_LIVE_OSC")
	if host == "" {
		t.Skip("set MAVOR_LIVE_OSC to a device address to run against real hardware")
	}
	requireNoRunningDaemon(t)
	paths := []string{"/ch/15/mix/on", "/ch/16/mix/on"}
	if p := os.Getenv("MAVOR_LIVE_OSC_PATHS"); p != "" {
		paths = strings.Split(p, ",")
	}

	d, err := NewOSCDucker(host, 0, paths, 0, DefaultOSCTimeoutMS*time.Millisecond)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = d.Close() })

	before, missing, err := d.Probe()
	if err != nil {
		t.Fatalf("Probe: %v", err)
	}
	if len(missing) > 0 {
		t.Fatalf("device did not answer for %v — is it powered on and at %s?", missing, d.Addr())
	}
	t.Logf("before: %v", before)

	// Whatever happens, leave the device the way it was found.
	t.Cleanup(func() {
		_ = d.Restore()
		if got, _, err := d.Probe(); err == nil {
			t.Logf("left as: %v", got)
		}
	})

	const cycles = 10
	var failures int
	for i := 1; i <= cycles; i++ {
		if err := d.Duck(); err != nil {
			t.Fatalf("cycle %d: Duck: %v", i, err)
		}
		time.Sleep(250 * time.Millisecond) // roughly a short dictation
		start := time.Now()
		if err := d.Restore(); err != nil {
			failures++
			t.Errorf("cycle %d: Restore: %v", i, err)
			continue
		}
		t.Logf("cycle %d: restored in %s", i, time.Since(start).Round(time.Microsecond))
	}
	if failures > 0 {
		t.Errorf("%d/%d restores failed to confirm — a read racing a write was reported as a failed restore", failures, cycles)
	}
}
