package daemon

import (
	"context"
	"log/slog"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/mschulkind-oss/mavor/internal/speech"
)

// perfSink collects the daemon's log lines. The daemon logs from the IPC
// handler, the decode worker and the perf ticker at once, so the collector needs
// its own lock rather than a bytes.Buffer.
type perfSink struct {
	mu    sync.Mutex
	lines []string
}

func (s *perfSink) Write(b []byte) (int, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.lines = append(s.lines, strings.TrimRight(string(b), "\n"))
	return len(b), nil
}

// perfLines returns the "perf: machine" lines written so far.
func (s *perfSink) perfLines() []string {
	s.mu.Lock()
	defer s.mu.Unlock()
	var out []string
	for _, line := range s.lines {
		if strings.Contains(line, `msg="perf: machine"`) {
			out = append(out, line)
		}
	}
	return out
}

// perfFixture writes a proc tree and one card's sysfs counters, so the readings
// in a test come from values the test chose rather than from the machine the
// test happens to run on.
func perfFixture(t *testing.T) (procRoot, sysRoot string) {
	t.Helper()
	procRoot, sysRoot = t.TempDir(), t.TempDir()
	write := func(path, content string) {
		t.Helper()
		if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
			t.Fatalf("mkdir %s: %v", filepath.Dir(path), err)
		}
		if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
			t.Fatalf("write %s: %v", path, err)
		}
	}
	write(filepath.Join(procRoot, "loadavg"), "4.25 3.10 2.00 2/812 4242\n")
	write(filepath.Join(procRoot, "meminfo"), "MemAvailable: 2621440 kB\nSwapFree: 0 kB\n")
	write(filepath.Join(procRoot, "stat"), "cpu  100 0 0 900 0 0 0 0 0 0\n")

	devDir := filepath.Join(sysRoot, "devices", "0000:03:00.0")
	write(filepath.Join(devDir, "mem_info_vram_total"), "17179869184\n")
	write(filepath.Join(devDir, "mem_info_vram_used"), "7516192768\n")
	write(filepath.Join(devDir, "mem_info_gtt_total"), "33669189632\n")
	write(filepath.Join(devDir, "mem_info_gtt_used"), "3489660928\n")
	write(filepath.Join(devDir, "gpu_busy_percent"), "96\n")
	write(filepath.Join(devDir, "pp_dpm_sclk"), "0: 500Mhz\n1: 2700Mhz *\n")
	cardDir := filepath.Join(sysRoot, "class", "drm", "card1")
	if err := os.MkdirAll(cardDir, 0o755); err != nil {
		t.Fatalf("mkdir card: %v", err)
	}
	if err := os.Symlink(filepath.Join("..", "..", "..", "devices", "0000:03:00.0"), filepath.Join(cardDir, "device")); err != nil {
		t.Fatalf("symlink card device: %v", err)
	}
	return procRoot, sysRoot
}

// waitForPerfLines waits until the sink holds a line satisfying pred.
func waitForPerfLine(t *testing.T, sink *perfSink, what string, pred func(string) bool) string {
	t.Helper()
	deadline := time.Now().Add(sendTimeout)
	for time.Now().Before(deadline) {
		for _, line := range sink.perfLines() {
			if pred(line) {
				return line
			}
		}
		time.Sleep(5 * time.Millisecond)
	}
	t.Fatalf("no perf line %s; got:\n%s", what, strings.Join(sink.perfLines(), "\n"))
	return ""
}

func phaseIs(phase string) func(string) bool {
	return func(line string) bool { return strings.Contains(line, "phase="+phase) }
}

// TestVerboseLogsMachineCountersAroundOneDictation is the feature: with verbose
// logging on, one dictation writes a reading at capture start, at capture stop
// and when the decode finishes, each carrying the host's load, the card's
// counters and the model's own footprint — so a slow dictation can be read
// against what the machine was doing at the time.
func TestVerboseLogsMachineCountersAroundOneDictation(t *testing.T) {
	sink := &perfSink{}
	procRoot, sysRoot := perfFixture(t)
	d, sock := newTestDaemon(t, func(c *Config) {
		c.Logger = slog.New(slog.NewTextHandler(sink, &slog.HandlerOptions{Level: slog.LevelDebug}))
		c.ProcRoot, c.SysRoot = procRoot, sysRoot
		c.Initialize = func(context.Context) (Initialized, error) {
			return Initialized{
				Transcriber: &speech.Mock{Text: "hello world"},
				MainModel:   "whisper-large-v3",
				Residency: func() (speech.Residency, speech.GPUMemory) {
					// A model resident in VRAM with a small GTT spill: the
					// reading a diagnosis is made from.
					return speech.ResidencyVRAM, speech.GPUMemory{
						VRAM: 3714158592, GTT: 2310144, Device: "0000:03:00.0",
					}
				},
			}, nil
		}
	})
	stop := runDaemon(t, d)
	defer stop()

	// Readiness, and the first residency reading, before the recording: a perf
	// line can only carry the model's footprint once the probe has answered.
	record(t, sock)
	deadline := time.Now().Add(sendTimeout)
	for {
		d.residencyMu.Lock()
		known := d.residency != speech.ResidencyUnknown
		d.residencyMu.Unlock()
		if known {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("residency probe never reported")
		}
		time.Sleep(5 * time.Millisecond)
	}

	sendWithRetry(t, sock, "stop")
	waitForState(t, sock, "idle")

	start := waitForPerfLine(t, sink, "at capture start", phaseIs("start"))
	stopLine := waitForPerfLine(t, sink, "at capture stop", phaseIs("stop"))
	done := waitForPerfLine(t, sink, "after the decode", phaseIs("done"))

	for _, want := range []string{
		"load1=4.25", "load5=3.1", "runnable=2",
		"mem_avail_mb=2560",
		"gpu=0000:03:00.0", "gpu_busy_pct=96", "gpu_clock_mhz=2700",
		"vram_used_mb=7168", "vram_total_mb=16384",
		"gtt_used_mb=3328", "gtt_total_mb=32109",
		"model_residency=vram", "model_vram_mb=3542",
		"cycle=1",
	} {
		if !strings.Contains(start, want) {
			t.Errorf("capture-start reading lacks %q:\n%s", want, start)
		}
	}
	if !strings.Contains(stopLine, "recording_ms=") {
		t.Errorf("capture-stop reading has no recording_ms:\n%s", stopLine)
	}
	for _, want := range []string{"phase=done", "transcribe_ms=", "total_ms="} {
		if !strings.Contains(done, want) {
			t.Errorf("decode reading lacks %q:\n%s", want, done)
		}
	}
}

// TestPerformanceLogIsOffWithoutVerbose is the other half of the gate: a daemon
// that is not logging debug detail writes no performance readings at all.
func TestPerformanceLogIsOffWithoutVerbose(t *testing.T) {
	sink := &perfSink{}
	procRoot, sysRoot := perfFixture(t)
	d, sock := newTestDaemon(t, func(c *Config) {
		c.Logger = slog.New(slog.NewTextHandler(sink, &slog.HandlerOptions{Level: slog.LevelInfo}))
		c.ProcRoot, c.SysRoot = procRoot, sysRoot
	})
	stop := runDaemon(t, d)
	defer stop()

	record(t, sock)
	sendWithRetry(t, sock, "stop")
	waitForState(t, sock, "idle")

	if got := sink.perfLines(); len(got) != 0 {
		t.Errorf("logged %d performance readings with debug off:\n%s", len(got), strings.Join(got, "\n"))
	}
}

// TestPeriodicPerformanceLogTicksWhileIdle covers the timer half: the log says
// what the machine is doing between dictations too, which is what a baseline for
// the next slow one is made of.
func TestPeriodicPerformanceLogTicksWhileIdle(t *testing.T) {
	sink := &perfSink{}
	procRoot, sysRoot := perfFixture(t)
	d, _ := newTestDaemon(t, func(c *Config) {
		c.Logger = slog.New(slog.NewTextHandler(sink, &slog.HandlerOptions{Level: slog.LevelDebug}))
		c.ProcRoot, c.SysRoot = procRoot, sysRoot
		c.PerfInterval = 5 * time.Millisecond
	})
	stop := runDaemon(t, d)
	defer stop()

	line := waitForPerfLine(t, sink, "from the timer", phaseIs("periodic"))
	if !strings.Contains(line, "state=idle") {
		t.Errorf("periodic reading = %s, want state=idle before any dictation", line)
	}
}
