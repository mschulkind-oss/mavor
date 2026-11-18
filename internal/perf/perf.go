// Package perf reads the machine's own performance counters — host load, memory
// pressure, and the GPU card's utilization and memory counters — so that a
// dictation's log can say what the machine was doing while it ran.
//
// Nothing here is about mavor's own work. It answers the question a slow
// dictation raises and mavor's own timings cannot: was the machine busy, was the
// card busy, and how much of the card's memory was somebody else holding. The
// model's own footprint is a different fact and comes from
// speech.GPUResidency, which attributes allocations to the process that holds
// them rather than to the whole card.
//
// Every reading is best-effort. A counter that cannot be read is left at zero
// rather than returning an error, because this runs beside the dictation path:
// a machine that withholds a counter must not lose a transcript.
package perf

import (
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
)

// GPU is one reading of a card's own counters, as sysfs reports them for the
// whole card: every process's allocations, plus the driver's utilization.
type GPU struct {
	// Device is the PCI id of the card these numbers came from, in the same
	// spelling as speech.GPUMemory.Device ("0000:03:00.0"), or "" when no card
	// reported counters.
	Device    string
	BusyPct   int
	VRAMUsed  uint64
	VRAMTotal uint64
	GTTUsed   uint64
	GTTTotal  uint64
	ClockMHz  int
}

// Present reports whether a card was found at all. A machine with no GPU, or
// one whose driver does not publish these counters, reads as absent rather than
// as a busy card at zero.
func (g GPU) Present() bool { return g.Device != "" }

// Sample is the machine at one moment.
type Sample struct {
	Load1     float64
	Load5     float64
	Load15    float64
	Runnable  int
	Processes int

	// CPUPercent is the whole machine's utilization since the previous Read of
	// the same Sampler, or -1 for the first reading, which has nothing to
	// subtract. Load averages are a minute of history; this is the interval.
	CPUPercent int

	MemAvailable uint64
	SwapFree     uint64

	GPU GPU
}

// Sampler reads the machine. Proc and Sys are roots — /proc and /sys in
// production — and are parameters so every parse is testable against a fixture
// tree rather than this machine's counters.
//
// A Sampler is not safe for concurrent use. It keeps one number between calls
// (the previous CPU time totals), so one goroutine owns it.
type Sampler struct {
	Proc string
	Sys  string

	prev     cpuTimes
	havePrev bool
}

// New returns a Sampler reading the given roots.
func New(procRoot, sysRoot string) *Sampler {
	return &Sampler{Proc: procRoot, Sys: sysRoot}
}

// Read takes one sample.
//
// device names the card whose counters are wanted, so the numbers belong to the
// card the model is actually on; when it is empty or no card reports that
// device, the first card publishing counters is read and the Sample names which
// one that was. Reporting another card's numbers would be worse than reporting
// none, so the caller always gets the device these numbers came from.
func (s *Sampler) Read(device string) Sample {
	var out Sample
	out.Load1, out.Load5, out.Load15, out.Runnable, out.Processes = readLoadAvg(s.Proc)
	out.MemAvailable, out.SwapFree = readMeminfo(s.Proc)
	out.CPUPercent = s.cpuPercent()
	out.GPU = readGPU(s.Sys, device)
	return out
}

// cpuTimes is one reading of the machine-wide CPU time counters.
type cpuTimes struct {
	total uint64
	idle  uint64
}

// cpuPercent takes the current CPU totals and reports the share of the interval
// that was not idle. Busy is everything the box did, including work that waits:
// a load average cannot separate a runnable queue from a blocked one, and this
// is the number that says whether the CPU was the scarce resource.
func (s *Sampler) cpuPercent() int {
	now, ok := readCPUTimes(s.Proc)
	if !ok {
		return -1
	}
	prev, hadPrev := s.prev, s.havePrev
	s.prev, s.havePrev = now, true
	if !hadPrev || now.total <= prev.total {
		return -1
	}
	totalDelta := now.total - prev.total
	idleDelta := now.idle - prev.idle
	if idleDelta > totalDelta {
		return 0
	}
	return int((totalDelta - idleDelta) * 100 / totalDelta)
}

// readLoadAvg parses /proc/loadavg: three load averages, then "runnable/total"
// and the last pid. A missing or malformed file reads as zero.
func readLoadAvg(procRoot string) (l1, l5, l15 float64, runnable, processes int) {
	data, err := os.ReadFile(filepath.Join(procRoot, "loadavg"))
	if err != nil {
		return 0, 0, 0, 0, 0
	}
	fields := strings.Fields(string(data))
	if len(fields) < 4 {
		return 0, 0, 0, 0, 0
	}
	l1, _ = strconv.ParseFloat(fields[0], 64)
	l5, _ = strconv.ParseFloat(fields[1], 64)
	l15, _ = strconv.ParseFloat(fields[2], 64)
	runnableText, processesText, ok := strings.Cut(fields[3], "/")
	if !ok {
		return l1, l5, l15, 0, 0
	}
	runnable, _ = strconv.Atoi(runnableText)
	processes, _ = strconv.Atoi(processesText)
	return l1, l5, l15, runnable, processes
}

// readMeminfo returns MemAvailable and SwapFree in bytes. Both are what a
// dictation competes for: page cache and free RAM for the model, and swap for
// everything else when there is none.
func readMeminfo(procRoot string) (available, swapFree uint64) {
	data, err := os.ReadFile(filepath.Join(procRoot, "meminfo"))
	if err != nil {
		return 0, 0
	}
	for line := range strings.Lines(string(data)) {
		key, value, ok := strings.Cut(line, ":")
		if !ok {
			continue
		}
		switch strings.TrimSpace(key) {
		case "MemAvailable":
			available = parseKB(value)
		case "SwapFree":
			swapFree = parseKB(value)
		}
	}
	return available, swapFree
}

// parseKB parses a /proc/meminfo value, which is a count in kilobytes.
func parseKB(value string) uint64 {
	fields := strings.Fields(value)
	if len(fields) == 0 {
		return 0
	}
	kb, err := strconv.ParseUint(fields[0], 10, 64)
	if err != nil {
		return 0
	}
	return kb << 10
}

// readCPUTimes parses the aggregate "cpu" line of /proc/stat. guest and
// guest_nice are excluded: the kernel already counts guest time in user and
// nice, and adding it twice would inflate the busiest intervals.
func readCPUTimes(procRoot string) (cpuTimes, bool) {
	data, err := os.ReadFile(filepath.Join(procRoot, "stat"))
	if err != nil {
		return cpuTimes{}, false
	}
	for line := range strings.Lines(string(data)) {
		if !strings.HasPrefix(line, "cpu ") {
			continue
		}
		fields := strings.Fields(line)[1:]
		var times cpuTimes
		for i, field := range fields {
			n, err := strconv.ParseUint(field, 10, 64)
			if err != nil {
				continue
			}
			// user nice system idle iowait irq softirq steal guest guest_nice
			if i > 7 {
				continue
			}
			times.total += n
			if i == 3 || i == 4 {
				times.idle += n
			}
		}
		return times, times.total > 0
	}
	return cpuTimes{}, false
}

// readGPU finds the card to report and reads its counters. A card is
// recognized by publishing its VRAM total: every other counter is optional and
// reads as zero when the driver does not have it, which is the same thing the
// absence means.
func readGPU(sysRoot, device string) GPU {
	cards, err := filepath.Glob(filepath.Join(sysRoot, "class", "drm", "card[0-9]*"))
	if err != nil {
		return GPU{}
	}
	sort.Strings(cards)

	// Wanted first, so a machine with two cards reports the one the model is
	// on; otherwise the first card that publishes counters, which is the one a
	// reader would have to look at anyway.
	var fallback GPU
	for _, card := range cards {
		g := readCard(card)
		if !g.Present() {
			continue
		}
		if device != "" && g.Device == device {
			return g
		}
		if !fallback.Present() {
			fallback = g
		}
	}
	return fallback
}

// readCard reads one /sys/class/drm/cardN. The device directory is a symlink
// whose basename is the PCI id, which is how a card is matched to the device
// speech.GPUResidency reports.
func readCard(card string) GPU {
	dev := filepath.Join(card, "device")
	total, ok := readUint(filepath.Join(dev, "mem_info_vram_total"))
	if !ok {
		return GPU{}
	}
	g := GPU{
		Device:    pciID(dev),
		VRAMTotal: total,
	}
	g.VRAMUsed, _ = readUint(filepath.Join(dev, "mem_info_vram_used"))
	g.GTTTotal, _ = readUint(filepath.Join(dev, "mem_info_gtt_total"))
	g.GTTUsed, _ = readUint(filepath.Join(dev, "mem_info_gtt_used"))
	if busy, ok := readUint(filepath.Join(dev, "gpu_busy_percent")); ok {
		g.BusyPct = int(busy)
	}
	g.ClockMHz = readCurrentClock(filepath.Join(dev, "pp_dpm_sclk"))
	return g
}

// pciID names the PCI device a card's device directory points at. sysfs makes
// it a relative symlink to a path ending in the id; a card that is a plain
// directory has none, and then the card is still readable but unnamed.
func pciID(devDir string) string {
	target, err := os.Readlink(devDir)
	if err != nil {
		return ""
	}
	return filepath.Base(target)
}

// readCurrentClock returns the graphics clock in MHz that the driver marks as
// current in pp_dpm_sclk — the line ending in "*" — or zero when the file is
// absent or nothing is marked. A card that stayed at its idle clock through a
// transcription is one that never did the work.
func readCurrentClock(path string) int {
	data, err := os.ReadFile(path)
	if err != nil {
		return 0
	}
	for line := range strings.Lines(string(data)) {
		fields := strings.Fields(line)
		if len(fields) < 2 || fields[len(fields)-1] != "*" {
			continue
		}
		mhz := strings.TrimSuffix(fields[1], "Mhz")
		mhz = strings.TrimSuffix(mhz, "MHz")
		n, err := strconv.Atoi(mhz)
		if err != nil {
			return 0
		}
		return n
	}
	return 0
}

// readUint reads one integer sysfs/proc file.
func readUint(path string) (uint64, bool) {
	data, err := os.ReadFile(path)
	if err != nil {
		return 0, false
	}
	n, err := strconv.ParseUint(strings.TrimSpace(string(data)), 10, 64)
	if err != nil {
		return 0, false
	}
	return n, true
}
