package main

import (
	"strings"
	"testing"
)

// The listing answers "will this model use my GPU", not "could this model use
// a GPU somewhere". These pin the difference.

func TestSherpaIsNeverGPUAcceleratedHoweverGoodTheHardware(t *testing.T) {
	// Every field set the way an ideal machine would set it.
	a := gpuAvailability{whisperBackend: "vulkan", devices: 4}
	if got := a.forEngine("sherpa"); got != "no" {
		t.Errorf("forEngine(sherpa) = %q, want %q — the vendored ONNX Runtime is CPU-only", got, "no")
	}
}

func TestWhisperReportsTheBackendItActuallyLoaded(t *testing.T) {
	a := gpuAvailability{whisperBackend: "vulkan", devices: 1}
	if got := a.forEngine("whisper"); got != "vulkan" {
		t.Errorf("forEngine(whisper) = %q, want %q", got, "vulkan")
	}
}

// A backend with no device still transcribes on the CPU, so reporting the
// backend name there would be a promise the machine does not keep.
func TestBackendWithoutADeviceIsNotAcceleration(t *testing.T) {
	a := gpuAvailability{whisperBackend: "vulkan", devices: 0}
	if got := a.forEngine("whisper"); got != "no" {
		t.Errorf("forEngine(whisper) = %q, want %q", got, "no")
	}
}

func TestNoBackendIsNo(t *testing.T) {
	a := gpuAvailability{whisperBackend: "", devices: 2}
	if got := a.forEngine("whisper"); got != "no" {
		t.Errorf("forEngine(whisper) = %q, want %q", got, "no")
	}
}

// A column of "no" is only useful if something says whose fault it is.
func TestFootnoteNamesTheReasonForEachWayOfHavingNoGPU(t *testing.T) {
	cases := []struct {
		name string
		a    gpuAvailability
		want string
	}{
		{"no backend", gpuAvailability{devices: 1}, "built with -DGGML_VULKAN=ON"},
		{"no device", gpuAvailability{whisperBackend: "vulkan"}, "no GPU device is visible"},
		{"working", gpuAvailability{whisperBackend: "vulkan", devices: 2}, "use the vulkan backend"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			got := c.a.footnote()
			if !strings.Contains(got, c.want) {
				t.Errorf("footnote() = %q, want it to mention %q", got, c.want)
			}
			if !strings.Contains(got, "sherpa") {
				t.Error("footnote never mentions sherpa, so its whole column of \"no\" goes unexplained")
			}
		})
	}
}

func TestGPUDetailBlamesTheRightThing(t *testing.T) {
	if got := gpuDetail(gpuAvailability{whisperBackend: "vulkan", devices: 1}, "sherpa"); !strings.Contains(got, "CPU-only") {
		t.Errorf("sherpa detail = %q, want it to blame the runtime", got)
	}
	if got := gpuDetail(gpuAvailability{}, "whisper"); !strings.Contains(got, "without a GPU backend") {
		t.Errorf("whisper detail = %q, want it to blame the build", got)
	}
	if got := gpuDetail(gpuAvailability{whisperBackend: "vulkan"}, "whisper"); !strings.Contains(got, "no GPU device") {
		t.Errorf("whisper detail = %q, want it to blame the missing device", got)
	}
}
