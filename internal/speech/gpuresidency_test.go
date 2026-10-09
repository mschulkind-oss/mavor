package speech

import (
	"os"
	"path/filepath"
	"strconv"
	"testing"
)

// These fixture strings are trimmed copies of what this machine's kernel
// prints for an amdgpu render node, so the parser is pinned to the real key
// names and units rather than to the shape the design assumed: the region is
// "vram" (not "vram0"), the size carries a binary unit suffix, and a zero is
// printed bare.
const (
	fdinfoResident = `pos:	0
flags:	0100002
mnt_id:	3211
ino:	613
drm-driver:	amdgpu
drm-client-id:	323
drm-pdev:	0000:03:00.0
pasid:	322
drm-total-cpu:	0
drm-total-gtt:	512 MiB
drm-resident-gtt:	512 MiB
drm-total-vram:	3100 MiB
drm-resident-vram:	3100 MiB
amd-evicted-vram:	0 KiB
amd-requested-vram:	3100 MiB
drm-memory-vram:	3100 KiB
drm-memory-gtt: 	2048 KiB
`
	fdinfoEvicted = `pos:	0
flags:	0100002
drm-driver:	amdgpu
drm-pdev:	0000:03:00.0
drm-total-vram:	300 MiB
drm-total-gtt:	3300 MiB
drm-memory-vram:	300 KiB
drm-memory-gtt:	3300 KiB
amd-evicted-vram:	3100 MiB
amd-requested-vram:	3400 MiB
`
)

func TestParseFdinfo(t *testing.T) {
	tests := []struct {
		name     string
		content  string
		want     drmUsage
		wantOK   bool
		wantNote string
	}{
		{
			name:    "amdgpu with the weights in VRAM",
			content: fdinfoResident,
			want:    drmUsage{pdev: "0000:03:00.0", vram: 3100 << 20, gtt: 512 << 20},
			wantOK:  true,
		},
		{
			name:    "amdgpu evicted into GTT",
			content: fdinfoEvicted,
			want:    drmUsage{pdev: "0000:03:00.0", vram: 300 << 20, gtt: 3300 << 20, evicted: 3100 << 20},
			wantOK:  true,
		},
		{
			// drm-total-* is the current spelling; drm-memory-* is the older
			// alias of the same stat, and preferring the current one is what
			// keeps a four-orders-of-magnitude difference out of the answer.
			name: "the current keys win over the aliases when both are present",
			content: "drm-driver: amdgpu\ndrm-pdev: 0000:03:00.0\n" +
				"drm-memory-vram: 3100 KiB\ndrm-total-vram: 3100 MiB\n",
			want:   drmUsage{pdev: "0000:03:00.0", vram: 3100 << 20},
			wantOK: true,
		},
		{
			name:    "the older alias alone still answers",
			content: "drm-driver: amdgpu\ndrm-memory-gtt: 2048 KiB\n",
			want:    drmUsage{gtt: 2048 << 10},
			wantOK:  true,
		},
		{
			name:    "a bare count is bytes, and a zero has no suffix",
			content: "drm-driver: amdgpu\ndrm-total-vram: 1073741824\ndrm-total-gtt: 0\n",
			want:    drmUsage{vram: 1073741824},
			wantOK:  true,
		},
		{
			name:    "the evicted counter is read when the kernel prints it",
			content: "drm-driver: amdgpu\ndrm-total-vram: 3400 MiB\namd-evicted-vram: 3100 MiB\n",
			want:    drmUsage{vram: 3400 << 20, evicted: 3100 << 20},
			wantOK:  true,
		},
		{
			name:    "another driver's device is not this card",
			content: "drm-driver: i915\ndrm-total-vram: 3100 MiB\n",
		},
		{
			// The proprietary NVIDIA driver does not report these keys; an
			// fdinfo entry with no DRM block at all is the common case.
			name:    "nvidia and every non-DRM file report nothing",
			content: "pos:\t0\nflags:\t0100002\nmnt_id:\t3211\n",
		},
		{
			name:    "an amdgpu entry with an unrecognized unit is not a size",
			content: "drm-driver: amdgpu\ndrm-total-vram: 3100 XB\n",
		},
		{
			name:    "an amdgpu entry with no size keys at all",
			content: "drm-driver: amdgpu\ndrm-pdev: 0000:03:00.0\n",
		},
		{
			name:    "garbage",
			content: "\x00\x01not: fdinfo\n",
		},
		{
			name:    "empty",
			content: "",
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got, ok := parseFdinfo(tc.content)
			if ok != tc.wantOK {
				t.Fatalf("parseFdinfo() ok = %v, want %v (%+v)", ok, tc.wantOK, got)
			}
			if got != tc.want {
				t.Errorf("parseFdinfo() = %+v, want %+v", got, tc.want)
			}
		})
	}
}

func TestSumDevicesCountsOneDeviceOnce(t *testing.T) {
	// A process holding both the card node and the render node of one device
	// holds two DRM files that report the same memory. Summing them would
	// double the model; the largest file is the device's answer.
	sameDevice := []drmUsage{
		{pdev: "0000:03:00.0", vram: 3100 << 20, gtt: 512 << 20},
		{pdev: "0000:03:00.0", vram: 3100 << 20, gtt: 512 << 20},
	}
	if got := sumDevices(sameDevice); got.VRAM != 3100<<20 || got.GTT != 512<<20 || got.Device != "0000:03:00.0" {
		t.Errorf("one device read twice = %+v, want it counted once", got)
	}

	// Two devices add, and the larger one names the card.
	twoDevices := []drmUsage{
		{pdev: "0000:03:00.0", vram: 100 << 20},
		{pdev: "0000:04:00.0", vram: 200 << 20, gtt: 1 << 20},
	}
	if got := sumDevices(twoDevices); got.VRAM != 300<<20 || got.GTT != 1<<20 || got.Device != "0000:04:00.0" {
		t.Errorf("two devices = %+v, want them summed with the larger named", got)
	}

	if got := sumDevices(nil); got != (GPUMemory{}) {
		t.Errorf("no files = %+v, want an empty footprint", got)
	}
}

func TestResidencyFrom(t *testing.T) {
	const model = 3100 << 20
	tests := []struct {
		name       string
		modelBytes uint64
		mem        GPUMemory
		want       Residency
	}{
		{
			name:       "weights in VRAM with room for the KV cache too",
			modelBytes: model,
			mem:        GPUMemory{VRAM: model + (343 << 20), GTT: 512 << 20},
			want:       ResidencyVRAM,
		},
		{
			name:       "a footprint exactly the size of the file is resident",
			modelBytes: model,
			mem:        GPUMemory{VRAM: model},
			want:       ResidencyVRAM,
		},
		{
			name:       "VRAM below the file is the eviction",
			modelBytes: model,
			mem:        GPUMemory{VRAM: 300 << 20, GTT: 3300 << 20},
			want:       ResidencyGTT,
		},
		{
			// amdgpu's own counter, on a kernel that reports a VRAM footprint
			// the size of the file plus its buffers: the weights are out even
			// though device memory still holds more than the file's size.
			name:       "a model's worth of evicted VRAM is the eviction too",
			modelBytes: model,
			mem:        GPUMemory{VRAM: model + (300 << 20), GTT: 1 << 20, Evicted: model + (100 << 20)},
			want:       ResidencyGTT,
		},
		{
			// A KV cache that spilled is not the weights: the note must not
			// claim the model was moved for it.
			name:       "a partial eviction leaves the weights where they are",
			modelBytes: model,
			mem:        GPUMemory{VRAM: model + (43 << 20), GTT: 343 << 20, Evicted: 343 << 20},
			want:       ResidencyVRAM,
		},
		{
			name:       "unknown model size decides nothing",
			modelBytes: 0,
			mem:        GPUMemory{VRAM: model},
			want:       ResidencyUnknown,
		},
		{
			name:       "a process with no DRM footprint decides nothing",
			modelBytes: model,
			mem:        GPUMemory{},
			want:       ResidencyUnknown,
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			if got := residencyFrom(tc.modelBytes, tc.mem); got != tc.want {
				t.Errorf("residencyFrom(%d, %+v) = %v, want %v", tc.modelBytes, tc.mem, got, tc.want)
			}
		})
	}
}

func TestGpuResidencyReadsAProcessTree(t *testing.T) {
	model := writeModel(t, 3100<<20)

	tests := []struct {
		name     string
		fdinfo   map[string]string
		want     Residency
		wantVRAM uint64
	}{
		{
			name:     "the weights are on the card",
			fdinfo:   map[string]string{"3": fdinfoResident},
			want:     ResidencyVRAM,
			wantVRAM: 3100 << 20,
		},
		{
			name:     "the weights were evicted into system RAM",
			fdinfo:   map[string]string{"3": fdinfoEvicted},
			want:     ResidencyGTT,
			wantVRAM: 300 << 20,
		},
		{
			name: "a socket, a pipe and a GPU file in one process",
			fdinfo: map[string]string{
				"0": "pos:\t0\nflags:\t0100002\n",
				"1": "pos:\t0\nflags:\t0100002\n",
				"4": fdinfoResident,
			},
			want:     ResidencyVRAM,
			wantVRAM: 3100 << 20,
		},
		{
			name:   "a child of another driver",
			fdinfo: map[string]string{"3": "drm-driver: i915\ndrm-total-vram: 8 GiB\n"},
			want:   ResidencyUnknown,
		},
		{
			name:   "no DRM files at all",
			fdinfo: map[string]string{"3": "pos:\t0\nflags:\t0100002\n"},
			want:   ResidencyUnknown,
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			procRoot := fixtureProcRoot(t, 4242, tc.fdinfo)
			got, mem := gpuResidency(procRoot, 4242, model)
			if got != tc.want {
				t.Fatalf("gpuResidency() = %v, want %v (footprint %+v)", got, tc.want, mem)
			}
			if mem.VRAM != tc.wantVRAM {
				t.Errorf("VRAM = %d, want %d", mem.VRAM, tc.wantVRAM)
			}
			if tc.want == ResidencyVRAM && mem.Device != "0000:03:00.0" {
				t.Errorf("Device = %q, want the pdev that holds the weights", mem.Device)
			}
		})
	}
}

// Every input the probe cannot read is the same answer, and that answer is not
// an error: the daemon paints a note from it and must never fail a dictation
// over it.
func TestGpuResidencyUnknownInputs(t *testing.T) {
	model := writeModel(t, 3100<<20)
	procRoot := fixtureProcRoot(t, 4242, map[string]string{"3": fdinfoResident})

	tests := []struct {
		name      string
		procRoot  string
		pid       int
		modelPath string
	}{
		{name: "no child", procRoot: procRoot, pid: 0, modelPath: model},
		{name: "a negative pid", procRoot: procRoot, pid: -1, modelPath: model},
		{name: "the pid is gone from the process table", procRoot: procRoot, pid: 9999, modelPath: model},
		{name: "a container without the host's /proc", procRoot: filepath.Join(t.TempDir(), "absent"), pid: 4242, modelPath: model},
		{name: "the model file is missing", procRoot: procRoot, pid: 4242, modelPath: filepath.Join(t.TempDir(), "absent.bin")},
		{name: "the model path is a directory", procRoot: procRoot, pid: 4242, modelPath: t.TempDir()},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got, _ := gpuResidency(tc.procRoot, tc.pid, tc.modelPath)
			if got != ResidencyUnknown {
				t.Errorf("gpuResidency() = %v, want unknown", got)
			}
		})
	}
}

func TestReadAmdgpuFootprintUnreadableInputs(t *testing.T) {
	if got := readAmdgpuFootprint(filepath.Join(t.TempDir(), "absent")); got != (GPUMemory{}) {
		t.Errorf("a missing fdinfo directory = %+v, want an empty footprint", got)
	}
	dir := t.TempDir()
	if err := os.Mkdir(filepath.Join(dir, "subdir"), 0o755); err != nil {
		t.Fatal(err)
	}
	if got := readAmdgpuFootprint(dir); got != (GPUMemory{}) {
		// A directory entry that is not a file is skipped, not an error.
		t.Errorf("a directory entry = %+v, want an empty footprint", got)
	}
}

func TestResidencyString(t *testing.T) {
	for _, tc := range []struct {
		in   Residency
		want string
	}{
		{ResidencyUnknown, "unknown"},
		{ResidencyVRAM, "vram"},
		{ResidencyGTT, "gtt"},
	} {
		if got := tc.in.String(); got != tc.want {
			t.Errorf("Residency(%d).String() = %q, want %q", tc.in, got, tc.want)
		}
	}
}

// The daemon asks for a probe through this, so what it answers for each
// placement is the whole contract: a warm GPU server is tracked, and everything
// with no long-lived GPU-resident child reports nothing to track.
func TestResidencyProbeOnlyForLocalServer(t *testing.T) {
	model := writeModel(t, 3100<<20)

	local := NewServerTranscriber("")
	local.Supervisor = NewSupervisor(SupervisorConfig{ModelPath: model})
	if probe := ResidencyProbe(WrapChunking(local, "auto", nil)); probe == nil {
		t.Fatal("a locally supervised server has weights to track")
	} else if got, _ := probe(); got != ResidencyUnknown {
		// Nothing has been started, so there is no pid yet.
		t.Errorf("an unstarted supervisor = %v, want unknown", got)
	}

	remote := NewServerTranscriber("http://example.invalid:8080")
	if probe := ResidencyProbe(remote); probe != nil {
		t.Error("a remote server's model is on another machine; nothing to track")
	}

	if probe := ResidencyProbe(&Mock{Text: "in process"}); probe != nil {
		t.Error("an in-process model holds no DRM file; nothing to track")
	}
	if probe := ResidencyProbe(nil); probe != nil {
		t.Error("no transcriber at all; nothing to track")
	}
}

// A supervisor that never started reports unknown rather than reading a pid it
// does not hold.
func TestSupervisorResidencyWithoutAChild(t *testing.T) {
	sup := NewSupervisor(SupervisorConfig{ModelPath: writeModel(t, 3100<<20)})
	if got, mem := sup.GPUResidency(); got != ResidencyUnknown || mem != (GPUMemory{}) {
		t.Errorf("GPUResidency() = %v %+v, want unknown", got, mem)
	}
	var nilSup *Supervisor
	if got, _ := nilSup.GPUResidency(); got != ResidencyUnknown {
		t.Errorf("a nil supervisor = %v, want unknown", got)
	}
}

// fixtureProcRoot lays out /proc/<pid>/fdinfo as a fixture tree, so the test
// never reads this machine's process table.
func fixtureProcRoot(t *testing.T, pid int, fdinfo map[string]string) string {
	t.Helper()
	dir := filepath.Join(t.TempDir(), "proc", strconv.Itoa(pid), "fdinfo")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	for name, content := range fdinfo {
		if err := os.WriteFile(filepath.Join(dir, name), []byte(content), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	return filepath.Join(filepath.Dir(filepath.Dir(dir)))
}

// writeModel creates a file of the given size. It is truncated rather than
// filled, because the rule reads the model's SIZE: a 3 GB fixture must not cost
// 3 GB of disk to test.
func writeModel(t *testing.T, size int64) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "ggml-model.bin")
	f, err := os.Create(path)
	if err != nil {
		t.Fatal(err)
	}
	if err := f.Truncate(size); err != nil {
		f.Close()
		t.Fatal(err)
	}
	if err := f.Close(); err != nil {
		t.Fatal(err)
	}
	return path
}
