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

func TestGPUReportForWhisper(t *testing.T) {
	cases := []struct {
		name     string
		cfg      config.Config
		backends []string
		devices  []string
		wantOK   bool
		contains string
	}{
		{
			// Not asking for the GPU is a valid, common setup. Reporting it
			// as a failure would train people to ignore doctor output.
			name:     "not requested, no backend",
			cfg:      config.Config{Engine: "cli", GPULayers: 0},
			wantOK:   true,
			contains: "CPU",
		},
		{
			name:     "not requested but available",
			cfg:      config.Config{Engine: "cli", GPULayers: 0},
			backends: []string{"vulkan"},
			devices:  []string{"/dev/dri/renderD128"},
			wantOK:   true,
			contains: "gpu_layers",
		},
		{
			name:     "requested and available",
			cfg:      config.Config{Engine: "cli", GPULayers: 32},
			backends: []string{"vulkan"},
			devices:  []string{"/dev/dri/renderD128"},
			wantOK:   true,
			contains: "vulkan",
		},
		{
			// The silent-CPU-fallback case: the config asks for offload the
			// binary cannot do, and whisper.cpp just runs on the CPU.
			name:     "requested but binary has no GPU backend",
			cfg:      config.Config{Engine: "cli", GPULayers: 32},
			backends: nil,
			devices:  []string{"/dev/dri/renderD128"},
			wantOK:   false,
			contains: "no GPU backend",
		},
		{
			name:     "requested, backend linked, but no device",
			cfg:      config.Config{Engine: "cli", GPULayers: 32},
			backends: []string{"vulkan"},
			devices:  nil,
			wantOK:   false,
			contains: "no GPU device",
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			ok, msg := gpuReport(tc.cfg, tc.backends, tc.devices)
			if ok != tc.wantOK {
				t.Errorf("gpuReport() ok = %v, want %v (msg: %s)", ok, tc.wantOK, msg)
			}
			if !strings.Contains(msg, tc.contains) {
				t.Errorf("gpuReport() msg = %q, want it to mention %q", msg, tc.contains)
			}
		})
	}
}

func TestGPUReportForSherpa(t *testing.T) {
	// The ONNX Runtime bundled with the sherpa-onnx Go binding carries no
	// provider libraries, so a GPU provider silently falls back to CPU.
	// Doctor has to say so rather than let the user believe it took effect.
	ok, msg := gpuReport(config.Config{Engine: "sherpa", SherpaProvider: "cuda"}, nil, []string{"/dev/dri/renderD128"})
	if ok {
		t.Errorf("a GPU sherpa_provider against the bundled CPU-only runtime should be flagged, got ok (msg: %s)", msg)
	}
	if !strings.Contains(msg, "cuda") {
		t.Errorf("message should name the configured provider, got %q", msg)
	}

	ok, msg = gpuReport(config.Config{Engine: "sherpa", SherpaProvider: "cpu"}, nil, nil)
	if !ok {
		t.Errorf("sherpa on CPU is a normal setup, got failure: %s", msg)
	}

	// ONNX Runtime has no Vulkan execution provider at all.
	ok, msg = gpuReport(config.Config{Engine: "sherpa", SherpaProvider: "vulkan"}, nil, nil)
	if ok {
		t.Errorf("vulkan is not an ONNX Runtime execution provider and should be flagged (msg: %s)", msg)
	}
}

// whisper.cpp 1.9+ loads its ggml backends dynamically and announces each one
// on startup. That announcement is the only trustworthy signal: a build can
// link libvulkan.so and still ship no libggml-vulkan.so, in which case it
// transcribes on the CPU no matter what -ngl says.
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
