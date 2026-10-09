package speech

import (
	"io"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
)

// Residency is where a supervised child's GPU allocations currently live.
//
// It answers the one question about the child that a user can act on: the main
// model was evicted from the card's VRAM into system RAM, so dictation works but
// is slower than the configuration promised. Everything the probe cannot answer
// is ResidencyUnknown — a result, never an error, so that no reading of /proc
// can fail a dictation.
type Residency int

const (
	// ResidencyUnknown means the probe could not say: no running child, a card
	// that does not report amdgpu usage, a process holding no DRM file, an
	// unreadable or missing /proc entry, or a build without /proc at all.
	// Callers must show nothing for it rather than guess.
	ResidencyUnknown Residency = iota
	// ResidencyVRAM means the model's weight footprint is in device memory.
	ResidencyVRAM
	// ResidencyGTT means it is not: the weights are in system RAM, which is
	// what an amdgpu eviction under VRAM pressure looks like from the owning
	// process's own fdinfo.
	ResidencyGTT
)

// String names the residency for logs and for the IPC status reply.
func (r Residency) String() string {
	switch r {
	case ResidencyVRAM:
		return "vram"
	case ResidencyGTT:
		return "gtt"
	}
	return "unknown"
}

// GPUMemory is one process's amdgpu footprint, summed over the DRM files it
// holds. Evicted is how much of its VRAM amdgpu says it has pushed out, on the
// kernels that report it (zero otherwise). Device is the PCI device holding the
// largest share, for logs only: on a machine with two cards the footprints add,
// and this says which one the model is on.
type GPUMemory struct {
	VRAM    uint64
	GTT     uint64
	Evicted uint64
	Device  string
}

// GPUResidency reports where the supervised child's weights currently live.
//
// It reads the child's own /proc/<pid>/fdinfo, which is the only source that
// attributes a footprint to the model rather than to the whole card: a browser
// or a game inflating VRAM appears in /sys/class/drm/*/device/mem_info_* and
// nowhere in this process's entries.
//
// It never fails. A stopped child, a non-amdgpu card, a process table that does
// not hold the child (a container with its own PID namespace), a missing model
// file and a kernel that does not print these keys all come back as
// ResidencyUnknown. No lock is held while reading, because the daemon's stop
// path must not wait on a probe.
func (s *Supervisor) GPUResidency() (Residency, GPUMemory) {
	if s == nil {
		return ResidencyUnknown, GPUMemory{}
	}
	s.mu.Lock()
	// An exited child's pid can be reused, and the process that gets it is not
	// this one: never look up a pid the supervisor is not still holding.
	pid := 0
	if s.isRunningLocked() {
		pid = s.cmd.Process.Pid
	}
	modelPath := s.cfg.ModelPath
	s.mu.Unlock()

	return gpuResidency("/proc", pid, modelPath)
}

// GPUResidency reports where this transcriber's supervised child keeps its
// weights. A remote server has no child here, so it has nothing to report.
func (s *ServerTranscriber) GPUResidency() (Residency, GPUMemory) {
	if s == nil || s.Supervisor == nil {
		return ResidencyUnknown, GPUMemory{}
	}
	return s.Supervisor.GPUResidency()
}

// ResidencyProbe returns the probe for t's model weights, or nil when there are
// none to track: a sherpa model is linked into the daemon and runs on the CPU
// with no DRM file at all, a subprocess transcriber spawns a second process per
// utterance and keeps no pid, and a remote server's model lives on the other
// machine. nil means "nothing to report", not "ask later".
func ResidencyProbe(t Transcriber) func() (Residency, GPUMemory) {
	st, ok := Unwrap(t).(*ServerTranscriber)
	if !ok || st.Supervisor == nil {
		return nil
	}
	return st.GPUResidency
}

// gpuResidency reads a process's amdgpu footprint and decides where the model
// at modelPath lives. procRoot is the process table's root — /proc in
// production — and is a parameter so the whole decision is testable against a
// fixture tree rather than this machine's process table.
//
// Nothing here returns an error. Each step that cannot be read leaves the
// answer Unknown, and Unknown is what the caller shows nothing for.
func gpuResidency(procRoot string, pid int, modelPath string) (Residency, GPUMemory) {
	if pid <= 0 {
		return ResidencyUnknown, GPUMemory{}
	}
	// The model's own file is the yardstick, so without it there is nothing to
	// compare a footprint against.
	info, err := os.Stat(modelPath)
	if err != nil || !info.Mode().IsRegular() {
		return ResidencyUnknown, GPUMemory{}
	}
	mem := readAmdgpuFootprint(filepath.Join(procRoot, strconv.Itoa(pid), "fdinfo"))
	return residencyFrom(uint64(info.Size()), mem), mem
}

// residencyFrom decides where a model's weights live from the size of its GGML
// file and the owning process's footprint on the card.
//
// The rule is anchored to the model rather than to a tuned VRAM/GTT ratio, and
// that is what lets it hold for a 75 MB tiny model and a 3 GB large-v3 alike:
// whisper.cpp uploads the weight file to the device as a single buffer, so
// while the model is resident VRAM holds at least the file's size — usually
// more, because the KV cache and the compute buffers are device memory too —
// and an amdgpu eviction moves that buffer out of device memory as a unit.
//
// Two statements about the same fact are consulted, because which of them a
// kernel makes is not something the caller can choose: a VRAM footprint below
// the file's size says the weights are not all in device memory, and amdgpu's
// own evicted-VRAM counter — printed on the kernels that have it — says how
// much device memory was pushed out. A whole model's worth of either is the
// weights; a smaller share is the KV cache or the compute buffers, which leaves
// the weights where the first statement found them.
//
// A model small enough that its compute buffers alone exceed the file is the
// case this reads as resident when it is not; an integrated GPU, where the
// weights are legitimately in shared memory from the start, reads as evicted.
// Both are why this is a note and never a failure.
func residencyFrom(modelBytes uint64, mem GPUMemory) Residency {
	if modelBytes == 0 || mem.VRAM+mem.GTT == 0 {
		return ResidencyUnknown
	}
	if mem.VRAM < modelBytes || mem.Evicted >= modelBytes {
		return ResidencyGTT
	}
	return ResidencyVRAM
}

// maxFdinfoBytes bounds one fdinfo read. The kernel's drm-usage-stats block is
// a few hundred bytes; this is a cap on a surprising entry, not a working size.
const maxFdinfoBytes = 64 * 1024

// readAmdgpuFootprint sums the DRM usage a process reports across its fdinfo
// entries. dir is /proc/<pid>/fdinfo in production and a fixture directory in
// tests. A directory that cannot be listed is an empty footprint rather than an
// error: this runs on a timer and has nowhere to report a failure to.
func readAmdgpuFootprint(dir string) GPUMemory {
	entries, err := os.ReadDir(dir)
	if err != nil {
		return GPUMemory{}
	}
	var files []drmUsage
	for _, e := range entries {
		f, err := os.Open(filepath.Join(dir, e.Name()))
		if err != nil {
			continue
		}
		content, err := io.ReadAll(io.LimitReader(f, maxFdinfoBytes))
		f.Close()
		if err != nil {
			continue
		}
		if u, ok := parseFdinfo(string(content)); ok {
			files = append(files, u)
		}
	}
	return sumDevices(files)
}

// drmUsage is one DRM file's usage stats: the device it belongs to, and the
// bytes that file holds there. Evicted is amdgpu's own evicted-VRAM counter,
// zero on a kernel that does not print it.
type drmUsage struct {
	pdev    string
	vram    uint64
	gtt     uint64
	evicted uint64
}

// sumDevices folds per-file usage into one process footprint.
//
// Within a device the largest file wins rather than the sum: a process that
// opened both /dev/dri/cardN and its render node holds two DRM files for the
// same device, and amdgpu reports the same allocations in both, so adding them
// would double the model. Across devices the footprints add, because those are
// different memory.
func sumDevices(files []drmUsage) GPUMemory {
	byPdev := make(map[string]drmUsage, len(files))
	for _, u := range files {
		best, seen := byPdev[u.pdev]
		if !seen || u.vram+u.gtt > best.vram+best.gtt {
			byPdev[u.pdev] = u
		}
	}
	pdevs := make([]string, 0, len(byPdev))
	for pdev := range byPdev {
		pdevs = append(pdevs, pdev)
	}
	// Sorted so that a tie for the largest device names the same one every
	// time; map order would otherwise leak into a logged field and a test.
	sort.Strings(pdevs)

	var total GPUMemory
	var biggest uint64
	for _, pdev := range pdevs {
		u := byPdev[pdev]
		total.VRAM += u.vram
		total.GTT += u.gtt
		total.Evicted += u.evicted
		if u.vram+u.gtt > biggest {
			biggest = u.vram + u.gtt
			total.Device = pdev
		}
	}
	return total
}

// parseFdinfo reads one /proc/<pid>/fdinfo entry. A file that is not an amdgpu
// DRM file — a socket, a pipe, a device of another driver — reports false
// rather than an empty footprint, because "not this device" and "nothing
// allocated" are different answers to the caller.
func parseFdinfo(content string) (drmUsage, bool) {
	fields := parseFdinfoFields(content)
	if fields["drm-driver"] != "amdgpu" {
		return drmUsage{}, false
	}
	vram, haveVRAM := firstSize(fields, "drm-total-vram", "drm-memory-vram")
	gtt, haveGTT := firstSize(fields, "drm-total-gtt", "drm-memory-gtt")
	if !haveVRAM && !haveGTT {
		return drmUsage{}, false
	}
	evicted, _ := parseSize(fields["amd-evicted-vram"])
	return drmUsage{pdev: fields["drm-pdev"], vram: vram, gtt: gtt, evicted: evicted}, true
}

// parseFdinfoFields splits an fdinfo entry into key/value pairs. The first
// occurrence of a key wins, which is how the two spellings of the same stat are
// kept apart below.
func parseFdinfoFields(content string) map[string]string {
	fields := make(map[string]string)
	for line := range strings.Lines(content) {
		key, value, ok := strings.Cut(line, ":")
		if !ok {
			continue
		}
		key = strings.TrimSpace(key)
		if _, seen := fields[key]; seen {
			continue
		}
		fields[key] = strings.TrimSpace(value)
	}
	return fields
}

// firstSize returns the first of names whose value parses as a size. The kernel
// prints the current drm-total-<region> keys and, for the drivers that have
// them, the older drm-memory-<region> aliases; either answers the question, so
// the current spelling is preferred and the alias is the fallback.
func firstSize(fields map[string]string, names ...string) (uint64, bool) {
	for _, name := range names {
		if v, ok := parseSize(fields[name]); ok {
			return v, true
		}
	}
	return 0, false
}

// sizeUnits are the suffixes drm-usage-stats sizes carry, as strings.
var sizeUnits = map[string]uint64{
	"B":   1,
	"KiB": 1 << 10,
	"MiB": 1 << 20,
	"GiB": 1 << 30,
	"TiB": 1 << 40,
}

// parseSize parses one drm-usage-stats size: a count with an optional unit
// suffix, printed as "3100 MiB", "12 KiB" and — for zero — a bare "0". A
// missing suffix is bytes. An unrecognized suffix is not a size: reporting
// nothing beats reporting a number whose units are a guess.
func parseSize(s string) (uint64, bool) {
	fields := strings.Fields(s)
	if len(fields) == 0 || len(fields) > 2 {
		return 0, false
	}
	n, err := strconv.ParseUint(fields[0], 10, 64)
	if err != nil {
		return 0, false
	}
	if len(fields) == 1 {
		return n, true
	}
	mult, ok := sizeUnits[fields[1]]
	if !ok {
		return 0, false
	}
	return n * mult, true
}
