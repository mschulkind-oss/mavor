package perf

import (
	"os"
	"path/filepath"
	"testing"
)

// fixture writes a proc tree and a sys tree for one test and returns the two
// roots. The values are this machine's spelling of each file, not a round
// number: the parsers are the thing under test.
type fixture struct {
	proc string
	sys  string
}

func newFixture(t *testing.T) *fixture {
	t.Helper()
	return &fixture{proc: t.TempDir(), sys: t.TempDir()}
}

func (f *fixture) sampler() *Sampler { return New(f.proc, f.sys) }

func writeFile(t *testing.T, path, content string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatalf("mkdir %s: %v", filepath.Dir(path), err)
	}
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatalf("write %s: %v", path, err)
	}
}

// host writes the three /proc files the sampler reads.
func (f *fixture) host(t *testing.T, loadavg, meminfo, stat string) {
	t.Helper()
	writeFile(t, filepath.Join(f.proc, "loadavg"), loadavg)
	writeFile(t, filepath.Join(f.proc, "meminfo"), meminfo)
	writeFile(t, filepath.Join(f.proc, "stat"), stat)
}

// card adds one GPU: a real directory under devices/<pdev> holding the counters,
// and class/drm/<card>/device symlinked to it, which is the shape sysfs has and
// the shape the PCI id is read from.
func (f *fixture) card(t *testing.T, card, pdev string, counters map[string]string) {
	t.Helper()
	devDir := filepath.Join(f.sys, "devices", pdev)
	for name, value := range counters {
		writeFile(t, filepath.Join(devDir, name), value+"\n")
	}
	if err := os.MkdirAll(filepath.Join(f.sys, "class", "drm", card), 0o755); err != nil {
		t.Fatalf("mkdir card: %v", err)
	}
	link := filepath.Join(f.sys, "class", "drm", card, "device")
	if err := os.Symlink(filepath.Join("..", "..", "..", "devices", pdev), link); err != nil {
		t.Fatalf("symlink %s: %v", link, err)
	}
}

const (
	idleStat = "cpu  100 0 0 900 0 0 0 0 0 0\ncpu0 100 0 0 900 0 0 0 0 0 0\n"
	busyStat = "cpu  200 0 0 925 0 0 0 0 0 0\ncpu0 200 0 0 925 0 0 0 0 0 0\n"
)

func TestReadParsesHostAndCard(t *testing.T) {
	f := newFixture(t)
	f.host(t,
		"0.52 0.58 0.59 3/1042 12345\n",
		"MemTotal:       65760140 kB\nMemAvailable:   34491104 kB\nSwapFree:        8388608 kB\n",
		idleStat)
	f.card(t, "card1", "0000:03:00.0", map[string]string{
		"mem_info_vram_total": "17179869184",
		"mem_info_vram_used":  "7196360704",
		"mem_info_gtt_total":  "33669189632",
		"mem_info_gtt_used":   "5387157504",
		"gpu_busy_percent":    "87",
		"pp_dpm_sclk":         "S: 72Mhz *\n0: 500Mhz\n1: 2700Mhz\n",
	})

	got := f.sampler().Read("0000:03:00.0")

	if got.Load1 != 0.52 || got.Load5 != 0.58 || got.Load15 != 0.59 {
		t.Errorf("load = %v/%v/%v, want 0.52/0.58/0.59", got.Load1, got.Load5, got.Load15)
	}
	if got.Runnable != 3 || got.Processes != 1042 {
		t.Errorf("procs = %d/%d, want 3 runnable of 1042", got.Runnable, got.Processes)
	}
	if got.MemAvailable != 34491104<<10 {
		t.Errorf("MemAvailable = %d, want %d", got.MemAvailable, uint64(34491104)<<10)
	}
	if got.SwapFree != 8388608<<10 {
		t.Errorf("SwapFree = %d, want %d", got.SwapFree, uint64(8388608)<<10)
	}
	if got.CPUPercent != -1 {
		t.Errorf("first reading CPUPercent = %d, want -1 (nothing to subtract)", got.CPUPercent)
	}
	gpu := got.GPU
	if !gpu.Present() || gpu.Device != "0000:03:00.0" {
		t.Fatalf("gpu device = %q (present %t), want 0000:03:00.0", gpu.Device, gpu.Present())
	}
	if gpu.BusyPct != 87 || gpu.ClockMHz != 72 {
		t.Errorf("gpu busy/clock = %d%%/%dMHz, want 87%%/72MHz", gpu.BusyPct, gpu.ClockMHz)
	}
	if gpu.VRAMUsed != 7196360704 || gpu.VRAMTotal != 17179869184 {
		t.Errorf("vram = %d/%d, want the file's own numbers", gpu.VRAMUsed, gpu.VRAMTotal)
	}
	if gpu.GTTUsed != 5387157504 || gpu.GTTTotal != 33669189632 {
		t.Errorf("gtt = %d/%d, want the file's own numbers", gpu.GTTUsed, gpu.GTTTotal)
	}
}

func TestCPUPercentMeasuresTheIntervalSinceThePreviousReading(t *testing.T) {
	f := newFixture(t)
	f.host(t, "0 0 0 1/1 1\n", "MemAvailable: 1 kB\n", idleStat)
	s := f.sampler()
	if got := s.Read(""); got.CPUPercent != -1 {
		t.Fatalf("first reading = %d%%, want -1", got.CPUPercent)
	}

	// 125 jiffies passed, 25 of them idle: the machine was 80% busy.
	f.host(t, "0 0 0 1/1 1\n", "MemAvailable: 1 kB\n", busyStat)
	if got := s.Read(""); got.CPUPercent != 80 {
		t.Errorf("second reading = %d%%, want 80", got.CPUPercent)
	}
}

func TestUnreadableCountersReadAsAbsent(t *testing.T) {
	f := newFixture(t) // no files at all
	got := f.sampler().Read("0000:03:00.0")
	if got.Load1 != 0 || got.MemAvailable != 0 || got.CPUPercent != -1 {
		t.Errorf("empty tree = %+v, want zeros and -1%%", got)
	}
	if got.GPU.Present() {
		t.Errorf("gpu = %+v, want absent with no card", got.GPU)
	}
}

func TestReadReportsTheNamedCard(t *testing.T) {
	f := newFixture(t)
	f.host(t, "0 0 0 1/1 1\n", "MemAvailable: 1 kB\n", idleStat)
	f.card(t, "card0", "0000:00:02.0", map[string]string{
		"mem_info_vram_total": "536870912",
		"gpu_busy_percent":    "4",
	})
	f.card(t, "card1", "0000:03:00.0", map[string]string{
		"mem_info_vram_total": "17179869184",
		"gpu_busy_percent":    "91",
	})
	// A card that publishes no VRAM total is not a card this can report on:
	// the counters are the only reason to read it, and it has none.
	writeFile(t, filepath.Join(f.sys, "class", "drm", "card2", "device", "uevent"), "DRIVER=vkms\n")

	s := f.sampler()
	if got := s.Read("0000:03:00.0"); got.GPU.Device != "0000:03:00.0" || got.GPU.BusyPct != 91 {
		t.Errorf("named card = %q busy %d%%, want 0000:03:00.0 at 91%%", got.GPU.Device, got.GPU.BusyPct)
	}
	if got := s.Read(""); got.GPU.Device != "0000:00:02.0" {
		t.Errorf("unnamed = %q, want the first card, 0000:00:02.0", got.GPU.Device)
	}
	// A named card that is gone must not silently become a different card's
	// numbers without saying whose they are.
	if got := s.Read("0000:99:00.0"); got.GPU.Device != "0000:00:02.0" {
		t.Errorf("missing named card = %q, want the fallback to name itself", got.GPU.Device)
	}
}

func TestClockIgnoresAnUnmarkedFile(t *testing.T) {
	f := newFixture(t)
	f.card(t, "card1", "0000:03:00.0", map[string]string{
		"mem_info_vram_total": "17179869184",
		"pp_dpm_sclk":         "0: 500Mhz\n1: 2700Mhz\n",
	})
	if got := f.sampler().Read("0000:03:00.0"); got.GPU.ClockMHz != 0 {
		t.Errorf("clock = %dMHz with nothing marked current, want 0", got.GPU.ClockMHz)
	}
}
