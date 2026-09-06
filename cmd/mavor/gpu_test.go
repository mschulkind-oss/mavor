package main

import (
	"strings"
	"testing"

	"github.com/mschulkind-oss/mavor/internal/config"
)

func TestDetectGPUBackendsFromLinkedLibraries(t *testing.T) {
	cases := []struct {
		name string
		ldd  string
		want []string
	}{
		{
			name: "cpu-only build",
			ldd: `	linux-vdso.so.1 (0x00007ffd8c7f5000)
	libggml.so => /usr/lib/libggml.so (0x00007f1e4c000000)
	libm.so.6 => /usr/lib/libm.so.6 (0x00007f1e4bf00000)
	libc.so.6 => /usr/lib/libc.so.6 (0x00007f1e4bd00000)`,
			want: nil,
		},
		{
			name: "vulkan build",
			ldd: `	libggml-vulkan.so => /usr/lib/libggml-vulkan.so (0x00007f1e4c000000)
	libvulkan.so.1 => /usr/lib/libvulkan.so.1 (0x00007f1e4be00000)
	libc.so.6 => /usr/lib/libc.so.6 (0x00007f1e4bd00000)`,
			want: []string{"vulkan"},
		},
		{
			name: "cuda build",
			ldd: `	libcudart.so.12 => /usr/local/cuda/lib64/libcudart.so.12 (0x00007f1e4c000000)
	libcublas.so.12 => /usr/local/cuda/lib64/libcublas.so.12 (0x00007f1e4bf00000)`,
			want: []string{"cuda"},
		},
		{
			name: "rocm build",
			ldd:  `	libhipblas.so.2 => /opt/rocm/lib/libhipblas.so.2 (0x00007f1e4c000000)`,
			want: []string{"rocm"},
		},
		{
			name: "both vulkan and cuda",
			ldd: `	libvulkan.so.1 => /usr/lib/libvulkan.so.1 (0x00007f1e4be00000)
	libcudart.so.12 => /usr/local/cuda/lib64/libcudart.so.12 (0x00007f1e4c000000)`,
			want: []string{"cuda", "vulkan"},
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := detectGPUBackends(tc.ldd)
			if strings.Join(got, ",") != strings.Join(tc.want, ",") {
				t.Errorf("detectGPUBackends() = %v, want %v", got, tc.want)
			}
		})
	}
}

// gpuReport reports what whisper.cpp loaded. It never advises a config
// setting, because there is no setting that turns acceleration on — the bug
// this replaced was doctor telling users to set gpu_layers, which made every
// transcription fail.

func TestGPUReportNoBackendIsNormal(t *testing.T) {
	ok, msg := gpuReport(config.Config{Model: "whisper-base.en"}, nil, nil)
	if !ok {
		t.Errorf("a machine with no GPU is a normal setup, got failure: %s", msg)
	}
	if !strings.Contains(msg, "CPU") {
		t.Errorf("msg = %q, want it to say the work happens on the CPU", msg)
	}
}

func TestGPUReportNamesTheLoadedBackendAndDeviceCount(t *testing.T) {
	ok, msg := gpuReport(config.Config{Model: "whisper-base.en"}, []string{"vulkan"}, []string{"/dev/dri/renderD128"})
	if !ok {
		t.Errorf("a loaded backend with a device behind it is healthy, got failure: %s", msg)
	}
	if !strings.Contains(msg, "vulkan") {
		t.Errorf("msg = %q, want it to name the loaded backend", msg)
	}
	if !strings.Contains(msg, "1 device") {
		t.Errorf("msg = %q, want it to report how many devices are visible", msg)
	}
}

// A backend with nothing behind it is the one genuine failure: whisper.cpp
// will fall back to the CPU and say nothing.
func TestGPUReportFailsWhenBackendLoadedButNoDevice(t *testing.T) {
	ok, msg := gpuReport(config.Config{Model: "whisper-base.en"}, []string{"vulkan"}, nil)
	if ok {
		t.Errorf("a loaded backend with no device should be flagged, got ok (msg: %s)", msg)
	}
	if !strings.Contains(msg, "no GPU device") {
		t.Errorf("msg = %q, want it to say no device is visible", msg)
	}
}

func TestGPUReportSaysWhenConfigDisabledTheGPU(t *testing.T) {
	ok, msg := gpuReport(
		config.Config{Model: "whisper-base.en", Advanced: config.Advanced{GPU: "off"}},
		[]string{"vulkan"},
		[]string{"/dev/dri/renderD128"},
	)
	if !ok {
		t.Errorf("turning the GPU off deliberately is not a fault, got failure: %s", msg)
	}
	if !strings.Contains(msg, "disabled by config") {
		t.Errorf("msg = %q, want it to say the config disabled the GPU", msg)
	}
}

// The old message told users to "set gpu_layers in config.toml to offload",
// which appended a flag whisper-cli rejects. No report may recommend a
// setting again, under any combination of backends and devices.
func TestGPUReportNeverAdvisesASetting(t *testing.T) {
	modelNames := []string{"whisper-base.en", "whisper-large-v3-turbo", "parakeet-tdt-0.6b"}
	gpus := []string{"", "auto", "off"}
	backendSets := [][]string{nil, {"vulkan"}, {"cuda", "vulkan"}}
	deviceSets := [][]string{nil, {"/dev/dri/renderD128"}}

	for _, model := range modelNames {
		for _, gpu := range gpus {
			for _, backends := range backendSets {
				for _, devices := range deviceSets {
					cfg := config.Config{Model: model, Advanced: config.Advanced{GPU: gpu}}
					_, msg := gpuReport(cfg, backends, devices)
					for _, banned := range []string{"gpu_layers", "-ngl"} {
						if strings.Contains(msg, banned) {
							t.Errorf("gpuReport(model=%q gpu=%q backends=%v devices=%v) mentions %q: %s",
								model, gpu, backends, devices, banned, msg)
						}
					}
				}
			}
		}
	}
}

// sherpa cannot use a GPU in this build at all: the sherpa-onnx Go binding
// vendors a CPU-only ONNX Runtime with no provider libraries. The report says
// so plainly rather than reasoning about a provider key.
func TestGPUReportForSherpaSaysCPU(t *testing.T) {
	ok, msg := gpuReport(config.Config{Model: "parakeet-tdt-0.6b"}, nil, nil)
	if !ok {
		t.Errorf("sherpa on the CPU is the only thing this build does; not a fault: %s", msg)
	}
	if !strings.Contains(msg, "CPU") {
		t.Errorf("msg = %q, want it to say sherpa runs on the CPU", msg)
	}
}

func TestGPUReportForSherpaMentionsUnusableDevices(t *testing.T) {
	ok, msg := gpuReport(config.Config{Model: "parakeet-tdt-0.6b"}, nil, []string{"/dev/dri/renderD128"})
	if !ok {
		t.Errorf("a GPU that sherpa cannot reach is not a fault: %s", msg)
	}
	if !strings.Contains(msg, "unusable") {
		t.Errorf("msg = %q, want it to say the present device cannot be used", msg)
	}
}

// The runtime is read off the model, not off a key: every sherpa model gets
// the same line, and there is no provider setting left to vary it.
func TestGPUReportIsTheSameForEverySherpaModel(t *testing.T) {
	_, first := gpuReport(config.Config{Model: "parakeet-tdt-0.6b"}, nil, nil)
	for _, model := range []string{"moonshine-tiny", "zipformer-streaming", "sensevoice-small"} {
		_, got := gpuReport(config.Config{Model: model}, nil, nil)
		if got != first {
			t.Errorf("sherpa report varied by model (%s):\n  %q\n  %q", model, first, got)
		}
	}
}

// whisper.cpp 1.9+ loads its ggml backends dynamically and announces each one
// on startup. That announcement is the only trustworthy signal: a build can
// link libvulkan.so and still ship no libggml-vulkan.so, in which case it
// transcribes on the CPU whatever the configuration says.
func TestParseLoadedBackends(t *testing.T) {
	cases := []struct {
		name string
		out  string
		want []string
	}{
		{
			name: "cpu-only build that nevertheless links libvulkan",
			out:  "load_backend: loaded CPU backend from /nix/store/abc-whisper-cpp-1.9.2/lib/libggml-cpu-haswell.so\n",
			want: nil,
		},
		{
			name: "vulkan build",
			out: "load_backend: loaded Vulkan backend from /usr/lib/libggml-vulkan.so\n" +
				"load_backend: loaded CPU backend from /usr/lib/libggml-cpu-haswell.so\n",
			want: []string{"vulkan"},
		},
		{
			name: "cuda build",
			out: "load_backend: loaded CUDA backend from /usr/lib/libggml-cuda.so\n" +
				"load_backend: loaded CPU backend from /usr/lib/libggml-cpu.so\n",
			want: []string{"cuda"},
		},
		{
			// BLAS is a CPU math library, not an accelerator.
			name: "blas is not a GPU backend",
			out: "load_backend: loaded BLAS backend from /usr/lib/libggml-blas.so\n" +
				"load_backend: loaded CPU backend from /usr/lib/libggml-cpu.so\n",
			want: nil,
		},
		{
			name: "no dynamic loading at all (older build)",
			out:  "usage: whisper-cli [options] file0 file1 ...\n",
			want: nil,
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := parseLoadedBackends(tc.out)
			if strings.Join(got, ",") != strings.Join(tc.want, ",") {
				t.Errorf("parseLoadedBackends() = %v, want %v", got, tc.want)
			}
		})
	}
}

// An older whisper.cpp compiled the backend straight in and prints no
// load_backend lines, so linkage is all we have. Newer builds must not be
// judged that way, because linkage lies.
func TestBackendDetectionPrefersRuntimeOverLinkage(t *testing.T) {
	dynamicCPUOnly := "load_backend: loaded CPU backend from /usr/lib/libggml-cpu-haswell.so\n"
	lddWithVulkan := "\tlibvulkan.so => /usr/lib/libvulkan.so (0x00007f1e4be00000)\n"

	got := resolveWhisperBackends(dynamicCPUOnly, lddWithVulkan)
	if len(got) != 0 {
		t.Errorf("a build that loaded only the CPU backend must report no GPU backend, got %v", got)
	}

	got = resolveWhisperBackends("usage: whisper-cli [options]\n", lddWithVulkan)
	if strings.Join(got, ",") != "vulkan" {
		t.Errorf("with no dynamic backends announced, linkage is the fallback signal; got %v", got)
	}
}
