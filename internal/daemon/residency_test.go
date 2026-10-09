package daemon

import (
	"context"
	"sync/atomic"
	"testing"
	"time"

	"github.com/mschulkind-oss/mavor/internal/overlay"
	"github.com/mschulkind-oss/mavor/internal/speech"
)

// residencyDaemon is a daemon whose initialization hands over a transcriber and
// a residency probe, which is how production wires the feature: the probe comes
// from the model that was just loaded, not from the daemon's configuration.
func residencyDaemon(t *testing.T, hud overlay.Overlay, probe func() (speech.Residency, speech.GPUMemory)) (*Daemon, string) {
	t.Helper()
	return newTestDaemon(t, func(c *Config) {
		c.Overlay = hud
		c.Initialize = func(context.Context) (Initialized, error) {
			return Initialized{Transcriber: &speech.Mock{Text: "hello world"}, MainModel: "whisper-base.en", Residency: probe}, nil
		}
	})
}

func record(t *testing.T, sock string) {
	t.Helper()
	// Readiness first: the models load in the background, and a start sent
	// while they are loading is answered without recording — which is the
	// point of the demand-only initialization notice.
	waitForState(t, sock, "idle")
	if got := sendWithRetry(t, sock, "start"); got.State != "recording" {
		t.Fatalf("start returned %q, want recording", got.State)
	}
}

func waitForNotice(t *testing.T, hud *overlay.Noop, want string) {
	t.Helper()
	deadline := time.Now().Add(sendTimeout)
	for time.Now().Before(deadline) {
		if hud.LastNotice() == want {
			return
		}
		time.Sleep(5 * time.Millisecond)
	}
	t.Fatalf("overlay notice = %q, want %q", hud.LastNotice(), want)
}

func evicted() (speech.Residency, speech.GPUMemory) {
	return speech.ResidencyGTT, speech.GPUMemory{VRAM: 300 << 20, GTT: 3300 << 20, Device: "0000:03:00.0"}
}

// The whole point of the feature: a model that has been pushed out of VRAM is
// visible while the user is speaking, and the line goes away with the
// recording.
func TestRecordingShowsResidencyNoticeAndClearsIt(t *testing.T) {
	hud := &overlay.Noop{}
	d, sock := residencyDaemon(t, hud, evicted)
	stop := runDaemon(t, d)
	defer stop()

	record(t, sock)
	waitForNotice(t, hud, residencyGTTNotice)

	sendWithRetry(t, sock, "toggle")
	waitForState(t, sock, "idle")
	waitForNotice(t, hud, "")
}

// A resident model, an unreadable one and a model with no GPU footprint at all
// are all "say nothing": the note is not a place for a guess.
func TestNoNoticeUnlessTheWeightsWereEvicted(t *testing.T) {
	nothing := func() (speech.Residency, speech.GPUMemory) { return speech.ResidencyUnknown, speech.GPUMemory{} }
	resident := func() (speech.Residency, speech.GPUMemory) {
		return speech.ResidencyVRAM, speech.GPUMemory{VRAM: 3400 << 20, Device: "0000:03:00.0"}
	}
	tests := map[string]func() (speech.Residency, speech.GPUMemory){
		"unknown":  nothing,
		"resident": resident,
		"no probe": nil,
	}
	for name, probe := range tests {
		t.Run(name, func(t *testing.T) {
			hud := &overlay.Noop{}
			var calls atomic.Int64
			var counted func() (speech.Residency, speech.GPUMemory)
			if probe != nil {
				counted = func() (speech.Residency, speech.GPUMemory) {
					calls.Add(1)
					return probe()
				}
			}
			d, sock := residencyDaemon(t, hud, counted)
			stop := runDaemon(t, d)
			defer stop()

			record(t, sock)
			// A nil probe is not sampled at all; the others are, and the test
			// waits for the sampling rather than for a wall-clock guess.
			deadline := time.Now().Add(sendTimeout)
			for calls.Load() == 0 && probe != nil && time.Now().Before(deadline) {
				time.Sleep(5 * time.Millisecond)
			}
			time.Sleep(100 * time.Millisecond)

			for _, n := range hud.Notices() {
				if n != "" {
					t.Fatalf("a notice was painted for a model that is not evicted: %q", n)
				}
			}
		})
	}
}

// doctor reads the same fact from the status reply, which is where a user
// without the HUD on screen finds it.
func TestStatusReportsTheResidency(t *testing.T) {
	tests := []struct {
		name  string
		probe func() (speech.Residency, speech.GPUMemory)
		want  string
	}{
		{name: "evicted", probe: evicted, want: "gtt"},
		{name: "resident", probe: func() (speech.Residency, speech.GPUMemory) {
			return speech.ResidencyVRAM, speech.GPUMemory{VRAM: 3400 << 20}
		}, want: "vram"},
		{name: "nothing to track", probe: nil, want: ""},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			d, sock := residencyDaemon(t, &overlay.Noop{}, tc.probe)
			stop := runDaemon(t, d)
			defer stop()

			waitForState(t, sock, "idle")

			deadline := time.Now().Add(sendTimeout)
			for {
				got := sendWithRetry(t, sock, "status")
				if got.Residency == tc.want {
					return
				}
				if time.Now().After(deadline) {
					t.Fatalf("status residency = %q, want %q", got.Residency, tc.want)
				}
				time.Sleep(5 * time.Millisecond)
			}
		})
	}
}

// The probe is on its own cadence, not the overlay's: it is a /proc read, and
// the level meter already ticks every 30 ms. One recording of a quarter second
// can only produce the reading taken when the recording started.
func TestResidencyProbeRunsOnItsOwnCadence(t *testing.T) {
	var calls atomic.Int64
	probe := func() (speech.Residency, speech.GPUMemory) {
		calls.Add(1)
		return speech.ResidencyVRAM, speech.GPUMemory{VRAM: 3400 << 20}
	}
	hud := &overlay.Noop{}
	d, sock := residencyDaemon(t, hud, probe)
	stop := runDaemon(t, d)
	defer stop()

	record(t, sock)
	deadline := time.Now().Add(sendTimeout)
	for calls.Load() == 0 && time.Now().Before(deadline) {
		time.Sleep(5 * time.Millisecond)
	}
	time.Sleep(200 * time.Millisecond)

	if n := calls.Load(); n < 1 || n > 3 {
		t.Fatalf("the probe ran %d times in a quarter second, want the reading the recording asked for (1) and at most one poll after it", n)
	}
}
