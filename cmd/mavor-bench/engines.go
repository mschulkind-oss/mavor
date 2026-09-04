package main

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"syscall"
	"time"
)

// backend names one column of the report: an engine, a device, and a decode
// mode. They are separate fields rather than one label because the report is
// pivoted on them — "every model on CPU" and "batch vs streaming for the
// models that stream" are both questions the JSON has to answer without
// string-matching a display name.
type backend struct {
	Engine string `json:"engine"` // "whisper-cli" or "sherpa"
	Device string `json:"device"` // "cpu" or "gpu"
	Mode   string `json:"mode"`   // "batch" or "streaming"

	// Build names which whisper.cpp produced the row: "stock" for whatever is
	// on PATH, "vulkan" for the out-of-tree GPU-capable build. Both appear in
	// the CPU column on purpose. The stock build is what a user actually gets
	// from their package manager, so it is the baseline worth publishing; the
	// Vulkan build run with -ng is the only fair thing to compare the GPU row
	// against, because it isolates the backend rather than the compiler flags,
	// the ggml version, and the -march the distro chose.
	Build string `json:"build,omitempty"`

	// Binary is the exact executable, so a reader can tell the two builds
	// apart without trusting the label.
	Binary string `json:"binary,omitempty"`
}

func (b backend) label() string {
	s := b.Engine + " / " + b.Device
	if b.Build != "" {
		s += " (" + b.Build + ")"
	}
	if b.Mode != "" && b.Mode != "batch" {
		s += " / " + b.Mode
	}
	return s
}

// runResult is one model on one backend: what it produced, how long it took,
// and how much memory it wanted. Timings are medians over the configured run
// count; Runs records how many actually completed so a partially failed cell
// cannot pass as a clean measurement.
type runResult struct {
	Model   string  `json:"model"`
	Family  string  `json:"family"`
	Backend backend `json:"backend"`

	Runs   int    `json:"runs"`
	Failed bool   `json:"failed"`
	Error  string `json:"error,omitempty"`

	// TotalMS is wall time for the whole transcription including model load,
	// which is what a user waits through on a cold dictation. LoadMS is
	// broken out where the engine reports it separately.
	TotalMS float64 `json:"total_ms"`
	LoadMS  float64 `json:"load_ms,omitempty"`

	// RTF is TotalMS over the audio duration. Below 1.0 is faster than real
	// time; the reciprocal is the "N times real time" figure.
	RTF float64 `json:"rtf"`

	// PeakRSSKB is the high-water mark of resident memory. For a subprocess
	// it is getrusage(RUSAGE_CHILDREN); for the in-process sherpa engine it
	// is the delta in this process's VmHWM across the run. The two are not
	// the same measurement and the report says which is which.
	PeakRSSKB int64 `json:"peak_rss_kb"`

	// FirstTokenMS is time to the first partial result, and only a streaming
	// backend has one. It is the number that decides whether a model feels
	// live while you speak, which total latency cannot tell you.
	FirstTokenMS float64 `json:"first_token_ms,omitempty"`

	Transcript string  `json:"transcript"`
	WER        float64 `json:"wer"`
	CER        float64 `json:"cer"`
	PunctDens  float64 `json:"punctuation_density"`
	CapF1      float64 `json:"capitalization_f1"`
}

// whisperRunner drives a whisper.cpp binary as a subprocess.
type whisperRunner struct {
	binary  string
	device  string
	build   string
	threads int

	// libPath goes on LD_LIBRARY_PATH. A whisper.cpp built out of tree finds
	// its ggml backends as shared objects at runtime, and without this it
	// silently loads whichever libggml-cpu it finds first on the system path
	// — producing a "GPU" column measured entirely on the CPU. That failure
	// is invisible in the timings and is exactly what the withdrawn reports
	// got wrong, so the harness sets it explicitly and verifies the backend
	// list afterwards.
	libPath string
}

// env returns the child environment: the Vulkan ICD when the loader needs
// pointing at one, and the build's own library directory ahead of the system
// path when this is an out-of-tree build.
func (w whisperRunner) env() []string {
	env := append(os.Environ(), vulkanEnv()...)
	if w.libPath != "" {
		existing := os.Getenv("LD_LIBRARY_PATH")
		joined := w.libPath
		if existing != "" {
			joined += ":" + existing
		}
		env = append(env, "LD_LIBRARY_PATH="+joined)
	}
	return env
}

// command builds the invocation. On a Vulkan-enabled binary, -ng is what
// forces the CPU path, so the CPU and GPU columns can come from the same
// build and differ in exactly one flag — the only way the comparison is
// honest about what changed.
func (w whisperRunner) command(ctx context.Context, modelPath, wavPath string) *exec.Cmd {
	args := []string{"-m", modelPath, "-f", wavPath, "-otxt", "-nt", "-np"}
	if w.device == "cpu" {
		args = append(args, "-ng")
	}
	if w.threads > 0 {
		args = append(args, "-t", fmt.Sprint(w.threads))
	}
	cmd := exec.CommandContext(ctx, w.binary, args...)
	cmd.Env = w.env()
	return cmd
}

// run transcribes wavPath once, returning the text, wall time, and the peak
// resident memory of the child process.
func (w whisperRunner) run(ctx context.Context, modelPath, wavPath string) (text string, elapsed time.Duration, peakKB int64, err error) {
	// whisper-cli writes <wav>.txt next to the input, so the run happens in a
	// scratch copy: benchmarking must not litter the fixtures directory, and
	// two backends writing the same .txt would race.
	tmpDir, err := os.MkdirTemp("", "mavor-bench-")
	if err != nil {
		return "", 0, 0, err
	}
	defer os.RemoveAll(tmpDir)

	scratchWav := filepath.Join(tmpDir, filepath.Base(wavPath))
	if err := copyFile(wavPath, scratchWav); err != nil {
		return "", 0, 0, err
	}

	cmd := w.command(ctx, modelPath, scratchWav)
	var stderr strings.Builder
	cmd.Stderr = &stderr

	start := time.Now()
	runErr := cmd.Run()
	elapsed = time.Since(start)

	if ru, ok := cmd.ProcessState.SysUsage().(*syscall.Rusage); ok {
		peakKB = int64(ru.Maxrss)
	}
	if runErr != nil {
		return "", elapsed, peakKB, fmt.Errorf("%s: %w: %s", w.binary, runErr, tailLines(stderr.String(), 3))
	}

	out, err := os.ReadFile(scratchWav + ".txt")
	if err != nil {
		return "", elapsed, peakKB, fmt.Errorf("whisper wrote no transcript: %w", err)
	}
	return strings.TrimSpace(string(out)), elapsed, peakKB, nil
}

// backends reports which ggml backends this whisper build actually brings up.
// It is printed in the report header, and it is the evidence for the GPU
// column: a build that brings up only a CPU backend cannot have produced a
// GPU number, however it was invoked.
//
// ggml announces itself two different ways depending on how it was built, and
// the harness has to read both. A build that loads its backends as separate
// shared objects at runtime — nixpkgs whisper-cpp does — prints
//
//	load_backend: loaded CPU backend from /nix/store/.../libggml-cpu-haswell.so
//
// while a build with a backend linked in prints that backend's own device
// probe instead, and no load_backend line at all:
//
//	ggml_vulkan: Found 1 Vulkan devices:
//
// Reading only the first form is why an earlier version of this check
// reported "no GPU backend" for a build that had just enumerated the card.
func (w whisperRunner) backends() []string {
	// The binary prints its backend probe on startup and then exits on the
	// missing input file, which is the cheapest way to make it talk.
	cmd := exec.Command(w.binary)
	cmd.Env = w.env()
	out, _ := cmd.CombinedOutput()

	seen := map[string]bool{}
	var found []string
	add := func(name string) {
		if name == "" || seen[name] {
			return
		}
		seen[name] = true
		found = append(found, name)
	}

	for _, line := range strings.Split(string(out), "\n") {
		switch {
		case strings.HasPrefix(line, "load_backend:"):
			// "load_backend: loaded Vulkan backend from /path/libggml-vulkan.so"
			if _, rest, ok := strings.Cut(line, "loaded "); ok {
				if name, _, ok := strings.Cut(rest, " backend"); ok {
					add(strings.TrimSpace(name))
				}
			}
		case strings.HasPrefix(line, "ggml_vulkan:"):
			add("Vulkan")
		case strings.HasPrefix(line, "ggml_cuda:"):
			add("CUDA")
		case strings.HasPrefix(line, "ggml_metal:"):
			add("Metal")
		case strings.HasPrefix(line, "ggml_sycl:"):
			add("SYCL")
		case strings.HasPrefix(line, "ggml_opencl:"):
			add("OpenCL")
		}
	}
	return found
}

func copyFile(src, dst string) error {
	data, err := os.ReadFile(src)
	if err != nil {
		return err
	}
	return os.WriteFile(dst, data, 0o644)
}

func tailLines(s string, n int) string {
	lines := strings.Split(strings.TrimSpace(s), "\n")
	if len(lines) > n {
		lines = lines[len(lines)-n:]
	}
	return strings.Join(lines, "; ")
}
