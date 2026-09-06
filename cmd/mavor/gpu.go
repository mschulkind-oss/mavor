package main

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"sort"
	"strings"

	"github.com/mschulkind-oss/mavor/internal/config"
	"github.com/mschulkind-oss/mavor/internal/models"
)

// GPU acceleration in mavor is a property of the build you are running, not of
// mavor itself, and it fails quietly: whisper.cpp uses a GPU when its build
// loaded a GPU backend and silently runs on the CPU when it did not, and ONNX
// Runtime falls back the same way when asked for a provider it has no library
// for. Nothing in the configuration can turn acceleration on — the only knob
// is `gpu = "off"`, which turns it off. So the checks here exist to report
// what actually loaded rather than to advise a setting.

// gpuLibraryMarkers maps a backend name to the shared libraries a whisper.cpp
// build links against when it is compiled with that backend.
var gpuLibraryMarkers = map[string][]string{
	"cuda":   {"libcudart", "libcublas", "libggml-cuda"},
	"vulkan": {"libvulkan", "libggml-vulkan"},
	"rocm":   {"libhipblas", "librocblas", "libggml-hip"},
	"sycl":   {"libsycl", "libggml-sycl"},
}

// detectGPUBackends reports which GPU backends a binary is linked against,
// given `ldd` output for it. A whisper.cpp build compiled without GPU support
// links none of them, which is exactly the case where transcription runs on
// the CPU whatever the configuration says.
func detectGPUBackends(ldd string) []string {
	var found []string
	for backend, markers := range gpuLibraryMarkers {
		for _, m := range markers {
			if strings.Contains(ldd, m) {
				found = append(found, backend)
				break
			}
		}
	}
	sort.Strings(found)
	return found
}

// cpuBackends are ggml backends that are not accelerators. BLAS is a CPU math
// library and CPU is the fallback every build carries.
var cpuBackends = map[string]bool{"cpu": true, "blas": true}

// parseLoadedBackends reads the "load_backend: loaded X backend from ..."
// lines whisper.cpp prints at startup and returns the GPU backends among them.
//
// This is the authoritative signal. Since ggml moved to dynamically loaded
// backends, a whisper.cpp package can link libvulkan.so and still ship no
// libggml-vulkan.so — it announces only the CPU backend and transcribes on the
// CPU. Linkage alone reports that build as GPU-capable.
func parseLoadedBackends(out string) []string {
	var found []string
	for _, line := range strings.Split(out, "\n") {
		_, rest, ok := strings.Cut(line, "load_backend: loaded ")
		if !ok {
			continue
		}
		name, _, ok := strings.Cut(rest, " backend")
		if !ok {
			continue
		}
		name = strings.ToLower(strings.TrimSpace(name))
		if name == "" || cpuBackends[name] || slices.Contains(found, name) {
			continue
		}
		found = append(found, name)
	}
	sort.Strings(found)
	return found
}

// resolveWhisperBackends decides what a whisper.cpp build can actually use.
// If it announced backends at startup it is a dynamic-loading build and that
// list is complete — linkage must not override it. Only a build that announced
// nothing is old enough to have compiled its backend in, and there linkage is
// the only signal left.
func resolveWhisperBackends(startup, ldd string) []string {
	if strings.Contains(startup, "load_backend:") {
		return parseLoadedBackends(startup)
	}
	return detectGPUBackends(ldd)
}

// whisperGPUBackends asks the whisper binary on PATH what it loaded. --help is
// enough to trigger backend loading, so this needs no model and no audio.
func whisperGPUBackends() []string {
	path, err := exec.LookPath("whisper-cli")
	if err != nil {
		if path, err = exec.LookPath("whisper-cpp"); err != nil {
			return nil
		}
	}

	// --help exits non-zero on some builds; the output is what matters.
	startup, _ := exec.Command(path, "--help").CombinedOutput()
	ldd, _ := exec.Command("ldd", path).CombinedOutput()
	return resolveWhisperBackends(string(startup), string(ldd))
}

// gpuDevices lists the GPU devices this machine exposes. A linked backend with
// no device behind it still means CPU execution.
func gpuDevices() []string {
	var devices []string

	// DRM render nodes cover AMD, Intel, and Nouveau, and are what a Vulkan
	// or ROCm build will open.
	if nodes, err := filepath.Glob("/dev/dri/renderD*"); err == nil {
		devices = append(devices, nodes...)
	}
	// The proprietary NVIDIA driver exposes character devices instead.
	if nodes, err := filepath.Glob("/dev/nvidia[0-9]*"); err == nil {
		devices = append(devices, nodes...)
	}

	filtered := devices[:0]
	for _, d := range devices {
		if _, err := os.Stat(d); err == nil {
			filtered = append(filtered, d)
		}
	}
	sort.Strings(filtered)
	return filtered
}

// checkGPU is the doctor entry point.
func checkGPU() (bool, string) {
	cfg, _ := config.Load("")
	return gpuReport(cfg, whisperGPUBackends(), gpuDevices())
}

// gpuReport turns the model's runtime, the backends whisper.cpp actually
// loaded, and the devices present into a single doctor line.
//
// It reports; it does not advise a setting. whisper.cpp offers no way to ask
// for a GPU — it uses one when its build has one — so the only thing a user
// can do here is install a build with a GPU backend, and the only thing the
// config can say is `gpu = "off"`. Having no GPU is a normal setup, so the
// only failure is a build that loaded a backend with no device behind it.
func gpuReport(cfg config.Config, backends, devices []string) (bool, string) {
	if models.RuntimeFor(cfg.Model) == models.RuntimeSherpa {
		return sherpaGPUReport(devices)
	}

	if cfg.GPUOff() {
		return true, fmt.Sprintf(
			"disabled by config (gpu = \"off\" passes -ng; whisper-cli loaded %s) — remove the key to use whatever this build has",
			describeBackends(backends))
	}

	switch {
	case len(backends) == 0:
		return true, "CPU only (whisper-cli loaded no GPU backend — the stock build ships CPU backends only; " +
			"install a whisper.cpp built with -DGGML_VULKAN=ON for acceleration)"

	case len(devices) == 0:
		return false, fmt.Sprintf(
			"whisper-cli loaded the %s backend but no GPU device is visible "+
				"(looked for /dev/dri/renderD* and /dev/nvidia*) — it will transcribe on the CPU",
			strings.Join(backends, "+"))

	default:
		return true, fmt.Sprintf("%s backend loaded by whisper-cli, %d device(s) visible — used automatically",
			strings.Join(backends, "+"), len(devices))
	}
}

// describeBackends names what whisper.cpp loaded, for a line that has to read
// well whether or not there was anything to name.
func describeBackends(backends []string) string {
	if len(backends) == 0 {
		return "no GPU backend anyway"
	}
	return "the " + strings.Join(backends, "+") + " backend"
}

// sherpaGPUReport states the one fact there is to state: this build cannot use
// a GPU for sherpa models. The sherpa-onnx Go binding vendors a CPU-only
// libonnxruntime.so with no execution-provider libraries beside it, and
// sherpa-onnx answers a provider it cannot honor by logging "Fallback to cpu!"
// and carrying on. See docs/design/configuration-surface.md §9.
func sherpaGPUReport(devices []string) (bool, string) {
	msg := "CPU (sherpa models run on the CPU in this build — the ONNX Runtime vendored by the " +
		"sherpa-onnx Go binding is CPU-only and ships no execution-provider libraries)"
	if len(devices) > 0 {
		msg += fmt.Sprintf(" — %d GPU device(s) present but unusable from here", len(devices))
	}
	return true, msg
}

// gpuAvailability is what THIS machine can accelerate, resolved once per
// command rather than once per model: probing whisper.cpp's backends shells
// out twice, and the answer is the same for all 25 catalog entries.
//
// It answers a question the catalog cannot, which is why it lives here and not
// in internal/models. Whether a model *can* use a GPU is a property of the
// model; whether it *will* on this machine is a property of the runtimes
// installed beside it, and only the second one is worth printing.
type gpuAvailability struct {
	// whisperBackend is the GPU backend whisper.cpp actually loaded, or ""
	// when it loaded none. The stock distribution build loads none.
	whisperBackend string
	// devices counts the render nodes visible to this process. A backend
	// with no device still transcribes on the CPU.
	devices int
}

func detectGPUAvailability() gpuAvailability {
	a := gpuAvailability{devices: len(gpuDevices())}
	if backends := whisperGPUBackends(); len(backends) > 0 {
		a.whisperBackend = strings.Join(backends, "+")
	}
	return a
}

// forEngine reports how a model of the given catalog engine runs on this
// machine: the backend's name when the GPU is genuinely used, "no" otherwise.
//
// sherpa is always "no", and that is a fact about the build rather than this
// machine: the sherpa-onnx-go module vendors a CPU-only ONNX Runtime with no
// provider shared objects, and sherpa-onnx answers a provider it cannot honour
// by logging "Fallback to cpu!" and continuing. Reporting anything else here
// would promise acceleration that silently does not happen.
func (a gpuAvailability) forEngine(engine string) string {
	if engine == "sherpa" {
		return "no"
	}
	if a.whisperBackend == "" || a.devices == 0 {
		return "no"
	}
	return a.whisperBackend
}

// gpuFootnote explains a column of "no" — without it a user reads the listing
// as "mavor cannot use my GPU" when the real answer is "the whisper.cpp you
// installed was built without one".
func (a gpuAvailability) footnote() string {
	switch {
	case a.whisperBackend == "":
		return "GPU: whisper-cli loaded no GPU backend, so whisper models run on the CPU " +
			"(install a whisper.cpp built with -DGGML_VULKAN=ON). sherpa models are CPU-only in this build."
	case a.devices == 0:
		return fmt.Sprintf("GPU: whisper-cli loaded the %s backend but no GPU device is visible, "+
			"so whisper models run on the CPU. sherpa models are CPU-only in this build.", a.whisperBackend)
	default:
		return fmt.Sprintf("GPU: whisper models use the %s backend (%d device(s)). "+
			"sherpa models are CPU-only in this build.", a.whisperBackend, a.devices)
	}
}
