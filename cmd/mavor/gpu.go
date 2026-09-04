package main

import (
	"cmp"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"sort"
	"strings"

	"github.com/mschulkind-oss/mavor/internal/config"
)

// GPU acceleration in mavor is a property of the build you are running, not of
// mavor itself, and it fails quietly: whisper.cpp given -ngl on a CPU-only build
// transcribes on the CPU without a word, and ONNX Runtime falls back the same
// way when asked for a provider it has no library for. The checks here exist
// to turn that silence into a doctor line.

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
// links none of them, which is exactly the case that makes -ngl a no-op.
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
// CPU, whatever -ngl is set to. Linkage alone reports that build as GPU-capable.
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

// gpuReport turns the configured engine, the backends the binary is linked
// against, and the devices present into a single doctor line. It fails only
// when the configuration asks for acceleration that cannot happen — having no
// GPU is a normal setup, not an error.
func gpuReport(cfg config.Config, backends, devices []string) (bool, string) {
	if cfg.Engine == "sherpa" {
		return sherpaGPUReport(cfg, devices)
	}

	switch {
	case cfg.GPULayers > 0 && len(backends) == 0:
		return false, fmt.Sprintf(
			"gpu_layers = %d but whisper-cli loaded no GPU backend — it will transcribe on the CPU "+
				"(fix: set gpu_layers = 0, or install a whisper.cpp built with -DGGML_VULKAN=ON)", cfg.GPULayers)

	case cfg.GPULayers > 0 && len(devices) == 0:
		return false, fmt.Sprintf(
			"gpu_layers = %d and whisper-cli loaded the %s backend, but no GPU device is visible "+
				"(looked for /dev/dri/renderD* and /dev/nvidia*) — it will transcribe on the CPU",
			cfg.GPULayers, strings.Join(backends, "+"))

	case cfg.GPULayers > 0:
		return true, fmt.Sprintf("%d layers offloaded via %s (%d device(s))",
			cfg.GPULayers, strings.Join(backends, "+"), len(devices))

	case len(backends) > 0 && len(devices) > 0:
		return true, fmt.Sprintf("available via %s but unused — set gpu_layers in config.toml to offload",
			strings.Join(backends, "+"))

	default:
		return true, "CPU only (whisper-cli loaded no GPU backend — the stock build ships CPU backends only)"
	}
}

// onnxGPUProviders are the execution providers ONNX Runtime can actually be
// built with. Vulkan is deliberately absent: ONNX Runtime has no Vulkan
// execution provider, so configuring one can only ever fall back to CPU.
var onnxGPUProviders = []string{"cuda", "coreml", "rocm", "tensorrt", "dml"}

func sherpaGPUReport(cfg config.Config, devices []string) (bool, string) {
	provider := cfg.SherpaProvider
	if provider == "" || provider == "cpu" {
		return true, fmt.Sprintf("CPU (sherpa_provider = %q; the ONNX Runtime bundled with the Go binding is a CPU-only build)",
			cmp.Or(provider, "cpu"))
	}

	if !slices.Contains(onnxGPUProviders, provider) {
		return false, fmt.Sprintf(
			"sherpa_provider = %q is not an ONNX Runtime execution provider — it will fall back to CPU "+
				"(valid: cpu, %s)", provider, strings.Join(onnxGPUProviders, ", "))
	}

	return false, fmt.Sprintf(
		"sherpa_provider = %q, but the ONNX Runtime bundled with the sherpa-onnx Go binding ships no provider "+
			"libraries and will fall back to CPU (%d GPU device(s) present; needs a runtime built against %s)",
		provider, len(devices), provider)
}
