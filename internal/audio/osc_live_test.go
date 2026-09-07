package audio

import (
	"os"
	"strings"
	"testing"
	"time"
)

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
